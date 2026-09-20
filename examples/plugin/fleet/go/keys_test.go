package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fleet-keys")
	if err != nil {
		panic(err)
	}
	grantFilePath = func() string { return filepath.Join(dir, "fleet-keys.json") }
	listProxyKeys = func(http.Header) ([]string, error) { return nil, nil }
	putProxyKeys = func(http.Header, []string) error { return nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestCatalogAllows(t *testing.T) {
	if !catalogAllows(nil, "openai/gpt-6-astra") {
		t.Fatal("nil grant must allow (legacy)")
	}
	all := &fleetKeyGrant{Mode: grantModeAll}
	if !catalogAllows(all, "xai/grok-4.6") {
		t.Fatal("all mode")
	}
	disabled := &fleetKeyGrant{Mode: grantModeAll, Disabled: true}
	if catalogAllows(disabled, "xai/grok-4.6") {
		t.Fatal("disabled must deny")
	}
	list := &fleetKeyGrant{Mode: grantModeList, Models: []string{"openai/gpt-6-astra", "xai/", "ocg/muse-spark-1.3-contributor"}}
	if !catalogAllows(list, "openai/gpt-6-astra") {
		t.Fatal("exact id")
	}
	if !catalogAllows(list, "xai/grok-4.6") {
		t.Fatal("prefix slash")
	}
	if catalogAllows(list, "or/deepseek-v4.1-flash") {
		t.Fatal("outside list")
	}
	star := &fleetKeyGrant{Mode: grantModeList, Models: []string{"ocg/*"}}
	if !catalogAllows(star, "ocg/muse-spark-1.3-contributor") {
		t.Fatal("star prefix")
	}
}

func TestKeyStoreRoundTripAndMode(t *testing.T) {
	secret := "sk-flt-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	g, err := normalizeGrant(fleetKeyGrant{
		ID:      "k_test",
		Name:    "pi",
		Prefix:  displayPrefix(secret),
		KeyHash: hashSecret(secret),
		Mode:    grantModeAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveGrantStore(fleetKeyStore{Grants: []fleetKeyGrant{g}}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(grantFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	got, err := grantForSecret(secret)
	if err != nil || got == nil || got.Name != "pi" {
		t.Fatalf("lookup = %+v err=%v", got, err)
	}
	if _, err := normalizeGrant(fleetKeyGrant{Mode: grantModeList}); err == nil {
		t.Fatal("empty list must fail")
	}
}

func TestCorruptGrantStore(t *testing.T) {
	if err := os.WriteFile(grantFilePath(), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGrantStore(); err == nil {
		t.Fatal("corrupt store must error")
	}
	if grantAllowsRequest("sk-flt-deadbeefdeadbeefdeadbeefdeadbeef", "openai/gpt-6-astra", "") {
		t.Fatal("corrupt store must fail closed for enforcement")
	}
	_ = os.Remove(grantFilePath())
}

func TestExtractAPIKeyFromQueryMetadata(t *testing.T) {
	secret := "sk-flt-queryqueryqueryqueryqueryqueryquery"
	got := extractAPIKey(nil, map[string]any{"query": map[string]any{"key": secret}})
	if got != secret {
		t.Fatalf("query key = %q", got)
	}
}

func TestFilterModelListLeavesEmbeddings(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"embedding":[0.1],"index":0},{"embedding":[0.2],"index":1}]}`)
	out := filterModelList(body, func(string) bool { return false })
	if string(out) != string(body) {
		t.Fatalf("embeddings mutated: %s", out)
	}
}

func TestModelsFilter(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"openai/gpt-6-astra","object":"model","created":1},{"id":"xai/grok-4.6","object":"model"}]}`)
	g := &fleetKeyGrant{Mode: grantModeList, Models: []string{"openai/"}}
	out := filterModelList(body, func(id string) bool { return catalogAllows(g, id) })
	var doc map[string]any
	if json.Unmarshal(out, &doc) != nil {
		t.Fatal("filter produced invalid json")
	}
	data := doc["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("len=%d body=%s", len(data), out)
	}
	if data[0].(map[string]any)["id"] != "openai/gpt-6-astra" {
		t.Fatalf("kept %v", data[0])
	}
	if doc["object"] != "list" {
		t.Fatal("envelope dropped")
	}
}

func TestInterceptDeniesOutOfGrant(t *testing.T) {
	secret := "sk-flt-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	g, _ := normalizeGrant(fleetKeyGrant{
		ID: "k_deny", Name: "limited", Prefix: displayPrefix(secret),
		KeyHash: hashSecret(secret), Mode: grantModeList, Models: []string{"openai/gpt-6-astra"},
	})
	if err := saveGrantStore(fleetKeyStore{Grants: []fleetKeyGrant{g}}); err != nil {
		t.Fatal(err)
	}
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "xai/grok-4.6",
		Headers:      http.Header{"Authorization": []string{"Bearer " + secret}},
		Body:         []byte(`{"model":"xai/grok-4.6","messages":[]}`),
	})
	if !resp.Terminate || resp.StatusCode != 403 {
		t.Fatalf("terminate=%v status=%d", resp.Terminate, resp.StatusCode)
	}
	if !strings.Contains(string(resp.ResponseBody), "model_not_allowed") {
		t.Fatalf("body=%s", resp.ResponseBody)
	}
	if strings.Contains(string(resp.ResponseBody), secret) {
		t.Fatal("secret leaked in deny body")
	}
	ok := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "openai/gpt-6-astra",
		Headers:      http.Header{"Authorization": []string{"Bearer " + secret}},
		Body:         []byte(`{"model":"openai/gpt-6-astra","messages":[{"role":"user","content":"hi"}]}`),
	})
	if ok.Terminate {
		t.Fatal("allowed model terminated")
	}
	_ = os.Remove(grantFilePath())
}

func TestModelsResponseInterceptFilters(t *testing.T) {
	secret := "sk-flt-ffffffffffffffffffffffffffffffff"
	g, _ := normalizeGrant(fleetKeyGrant{
		ID: "k_models", Name: "list", Prefix: displayPrefix(secret),
		KeyHash: hashSecret(secret), Mode: grantModeList, Models: []string{"openai/"},
	})
	if err := saveGrantStore(fleetKeyStore{Grants: []fleetKeyGrant{g}}); err != nil {
		t.Fatal(err)
	}
	req := pluginapi.ResponseInterceptRequest{
		RequestHeaders:  http.Header{"Authorization": []string{"Bearer " + secret}},
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		Body:            []byte(`{"object":"list","data":[{"id":"openai/gpt-6-astra"},{"id":"xai/grok-4.6"}]}`),
	}
	raw, _ := json.Marshal(req)
	out, err := handleMethod(pluginabi.MethodResponseInterceptAfter, raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("%s", out)
	}
	var resp pluginapi.ResponseInterceptResponse
	if json.Unmarshal(env.Result, &resp) != nil {
		t.Fatal("result")
	}
	if strings.Contains(string(resp.Body), "xai/grok-4.6") {
		t.Fatalf("did not filter: %s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "openai/gpt-6-astra") {
		t.Fatalf("dropped allowed: %s", resp.Body)
	}
	_ = os.Remove(grantFilePath())
}

func TestInterceptLegacyUnrestricted(t *testing.T) {
	_ = os.Remove(grantFilePath())
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "xai/grok-4.6",
		Headers:      http.Header{"Authorization": []string{"Bearer sk-legacy-not-a-grant"}},
		Body:         []byte(`{"model":"xai/grok-4.6","messages":[{"role":"user","content":"hi"}]}`),
	})
	if resp.Terminate {
		t.Fatal("legacy key must stay unrestricted")
	}
}

func dispatchKeys(t *testing.T, method, path string, query map[string][]string, body any) pluginapi.ManagementResponse {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method:  method,
		Path:    path,
		Headers: http.Header{"Authorization": []string{"Bearer mgmt"}},
		Query:   query,
		Body:    raw,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("envelope %s", out)
	}
	var resp pluginapi.ManagementResponse
	if json.Unmarshal(env.Result, &resp) != nil {
		t.Fatal("result")
	}
	return resp
}

func TestFleetKeyCreateListRestrictRevoke(t *testing.T) {
	_ = os.Remove(grantFilePath())
	var proxy []string
	listProxyKeys = func(http.Header) ([]string, error) { return append([]string{}, proxy...), nil }
	putProxyKeys = func(_ http.Header, keys []string) error {
		proxy = append([]string{}, keys...)
		return nil
	}
	generateSecret = func() (string, error) { return "sk-flt-cccccccccccccccccccccccccccccccc", nil }
	generateGrantID = func() (string, error) { return "k_fixed", nil }

	created := dispatchKeys(t, http.MethodPost, "/v0/management/fleet/keys", nil, map[string]any{
		"name": "pi", "mode": "all",
	})
	if created.StatusCode != 200 {
		t.Fatalf("create status %d %s", created.StatusCode, created.Body)
	}
	var createDoc map[string]any
	if json.Unmarshal(created.Body, &createDoc) != nil {
		t.Fatal(created.Body)
	}
	if createDoc["secret"] != "sk-flt-cccccccccccccccccccccccccccccccc" {
		t.Fatalf("secret once = %v", createDoc["secret"])
	}
	if len(proxy) != 1 {
		t.Fatalf("proxy keys = %d", len(proxy))
	}

	listed := dispatchKeys(t, http.MethodGet, "/v0/management/fleet/keys", nil, nil)
	if strings.Contains(string(listed.Body), "sk-flt-cccccccccccccccccccccccccccccccc") {
		t.Fatal("GET leaked secret")
	}
	if !strings.Contains(string(listed.Body), "sk-flt-…") && !strings.Contains(string(listed.Body), "prefix") {
		t.Fatalf("list missing prefix: %s", listed.Body)
	}

	patched := dispatchKeys(t, http.MethodPatch, "/v0/management/fleet/keys", nil, map[string]any{
		"id": "k_fixed", "mode": "list", "models": []string{"openai/gpt-6-astra"},
	})
	if patched.StatusCode != 200 {
		t.Fatalf("patch %d %s", patched.StatusCode, patched.Body)
	}

	revoked := dispatchKeys(t, http.MethodDelete, "/v0/management/fleet/keys", map[string][]string{"id": {"k_fixed"}}, nil)
	if revoked.StatusCode != 200 {
		t.Fatalf("revoke %d %s", revoked.StatusCode, revoked.Body)
	}
	if len(proxy) != 0 {
		t.Fatalf("proxy still has %v", proxy)
	}
}

func TestFleetKeyAdopt(t *testing.T) {
	_ = os.Remove(grantFilePath())
	legacy := "sk-legacy-dddddddddddddddddddddddddddddddd"
	proxy := []string{legacy}
	listProxyKeys = func(http.Header) ([]string, error) { return append([]string{}, proxy...), nil }
	putProxyKeys = func(_ http.Header, keys []string) error {
		proxy = append([]string{}, keys...)
		return nil
	}
	adopted := dispatchKeys(t, http.MethodPost, "/v0/management/fleet/keys/adopt", nil, map[string]any{
		"index": 0, "name": "harness", "mode": "all",
	})
	if adopted.StatusCode != 200 {
		t.Fatalf("adopt %d %s", adopted.StatusCode, adopted.Body)
	}
	if len(proxy) != 1 || proxy[0] != legacy {
		t.Fatalf("adopt rotated secret: %v", proxy)
	}
	listed := dispatchKeys(t, http.MethodGet, "/v0/management/fleet/keys", nil, nil)
	if strings.Contains(string(listed.Body), legacy) {
		t.Fatal("adopt list leaked secret")
	}
	if !strings.Contains(string(listed.Body), "harness") {
		t.Fatalf("adopt missing name: %s", listed.Body)
	}
}

func TestFleetKeyCreateRollsBackGrantOnProxyFailure(t *testing.T) {
	_ = os.Remove(grantFilePath())
	listProxyKeys = func(http.Header) ([]string, error) { return nil, nil }
	putProxyKeys = func(http.Header, []string) error { return os.ErrPermission }
	generateSecret = func() (string, error) { return "sk-flt-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", nil }
	generateGrantID = func() (string, error) { return "k_rollback", nil }
	resp := dispatchKeys(t, http.MethodPost, "/v0/management/fleet/keys", nil, map[string]any{"name": "x", "mode": "all"})
	if resp.StatusCode == 200 {
		t.Fatal("create should fail")
	}
	store, err := loadGrantStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Grants) != 0 {
		t.Fatalf("orphan grant: %+v", store.Grants)
	}
}

func TestKeysPageIsHTMLShell(t *testing.T) {
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/fleet/keys",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("envelope %s", out)
	}
	var resp pluginapi.ManagementResponse
	if json.Unmarshal(env.Result, &resp) != nil {
		t.Fatal("result")
	}
	page := string(resp.Body)
	if !strings.Contains(page, "Mint a key") {
		t.Fatal("keys shell missing mint copy")
	}
	if strings.Contains(page, `"key_hash"`) {
		t.Fatal("keys shell leaked grant store")
	}
	if !strings.Contains(page, "/v0/management") || !strings.Contains(page, "/fleet/keys") {
		t.Fatal("keys shell must fetch key-gated JSON")
	}
	if !strings.Contains(page, `class="here"`) || !strings.Contains(page, "Hub") {
		t.Fatal("keys nav missing switchboard links")
	}
}
