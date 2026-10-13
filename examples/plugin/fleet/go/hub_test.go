package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHubQuotaOmitsUnknownElapsed(t *testing.T) {
	freezeNow(t)
	state.mu.Lock()
	oldQuota, oldCache := state.quota, state.quotaCache
	state.quota = fakeQuota{snap: map[string]providerQuota{
		"test": {Resources: map[string]quotaResource{
			"unknown": {Kind: "consumption", Remaining: 75, Limit: 100},
			"known":   {Kind: "consumption", Remaining: 75, Limit: 100, WindowSeconds: 100, ResetsAt: testNow.Add(50 * time.Second)},
			"start":   {Kind: "consumption", Remaining: 100, Limit: 100, WindowSeconds: 100, ResetsAt: testNow.Add(100 * time.Second)},
		}},
	}}
	state.quotaCache.at = time.Time{}
	state.quotaCache.fetching = false
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.quota, state.quotaCache = oldQuota, oldCache
		state.mu.Unlock()
	})

	providers := buildQuota()
	if len(providers) != 1 || len(providers[0].Resources) != 3 {
		t.Fatalf("quota shape = %+v", providers)
	}
	seen := make(map[string]bool)
	for _, resource := range providers[0].Resources {
		seen[resource.Name] = true
		encoded, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		switch resource.Name {
		case "unknown":
			if strings.Contains(string(encoded), `"elapsed_frac"`) {
				t.Fatal("missing elapsed marker must be absent from quota JSON")
			}
		case "known":
			if !strings.Contains(string(encoded), `"elapsed_frac":0.5`) {
				t.Fatal("known elapsed marker must retain its computed value")
			}
		case "start":
			if !strings.Contains(string(encoded), `"elapsed_frac":0`) {
				t.Fatal("known zero elapsed must be present, not treated as missing")
			}
		}
	}
	if !seen["unknown"] || !seen["known"] || !seen["start"] {
		t.Fatalf("quota resource names = %+v", seen)
	}
}

func TestManagementDispatchRejectsSuffixMatch(t *testing.T) {
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/fleet/not/hub",
	}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || env.OK {
		t.Fatalf("expected not_found, got %s", out)
	}
}

func TestLegacyFleetResourceRedirectsToNativeRoute(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/v0/resource/plugins/fleet/hub", "/management.html#/fleet"},
		{"/v0/resource/plugins/fleet/keys", "/management.html#/fleet/access"},
		{"/v0/resource/plugins/fleet/savings", "/management.html#/fleet/activity"},
		{"/v0/resource/plugins/fleet/router", "/management.html#/fleet"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			out, err := managementDispatch(&pluginapi.ManagementRequest{Method: http.MethodGet, Path: tt.path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var env envelope
			if err := json.Unmarshal(out, &env); err != nil || !env.OK {
				t.Fatalf("envelope: %v %s", err, out)
			}
			var resp pluginapi.ManagementResponse
			if err := json.Unmarshal(env.Result, &resp); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(resp.Body), tt.want) || strings.Contains(string(resp.Body), "<iframe") {
				t.Fatalf("legacy page did not redirect directly to native route: %s", resp.Body)
			}
		})
	}
}

func TestManagementDispatchStateOmitsRouter(t *testing.T) {
	oldHostHTTP := hostHTTP
	hostHTTP = func(pluginapi.HTTPRequest) (*pluginapi.HTTPResponse, error) {
		return &pluginapi.HTTPResponse{StatusCode: http.StatusServiceUnavailable}, nil
	}
	t.Cleanup(func() { hostHTTP = oldHostHTTP })
	state.mu.Lock()
	oldQuota, oldCache := state.quota, state.quotaCache
	state.quota = fakeQuota{snap: map[string]providerQuota{}}
	state.quotaCache = struct {
		at       time.Time
		snap     map[string]providerQuota
		err      error
		fetching bool
	}{}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.quota, state.quotaCache = oldQuota, oldCache
		state.mu.Unlock()
	})

	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/fleet/state",
	}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope: %v %s", err, out)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("result: %v", err)
	}
	var st hubState
	if err := json.Unmarshal(resp.Body, &st); err != nil {
		t.Fatalf("state decode: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	flags := body["flags"].(map[string]any)
	if _, exists := flags["router"]; exists {
		t.Fatalf("Fleet state exposes a router flag: %s", resp.Body)
	}
	for _, provider := range st.Providers {
		if isLegacyFleetProvider("", provider.Name) {
			t.Fatalf("Fleet state exposes a synthetic provider: %+v", provider)
		}
	}
}

func TestManagementDispatchUnknown(t *testing.T) {
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/fleet/nope",
	}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || env.OK {
		t.Fatalf("expected error envelope, got %s", out)
	}
	if env.Error.Code != "not_found" {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestPxpipeScopeToggle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var pushed []bool
	orig := hostHTTP
	hostHTTP = func(req pluginapi.HTTPRequest) (*pluginapi.HTTPResponse, error) {
		var body struct {
			On bool `json:"on"`
		}
		_ = json.Unmarshal(req.Body, &body)
		pushed = append(pushed, body.On)
		return &pluginapi.HTTPResponse{StatusCode: 200}, nil
	}
	defer func() { hostHTTP = orig }()

	out, err := pxpipeScopeToggle([]byte(`{"model":"grok-4.6","on":true}`))
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("envelope: %s", out)
	}
	scope, src := pxpipeScope()
	if src != "config.json" {
		t.Fatalf("scope source = %s", src)
	}
	var hasGrok bool
	for _, m := range scope {
		if m == "grok-4.6" {
			hasGrok = true
		}
	}
	if !hasGrok {
		t.Fatalf("scope = %v, want grok-4.6 present", scope)
	}
	if len(pushed) != 1 || !pushed[0] {
		t.Fatalf("live push = %v", pushed)
	}
}

func TestPxpipeScopeToggleReportsLivePushFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := hostHTTP
	hostHTTP = func(pluginapi.HTTPRequest) (*pluginapi.HTTPResponse, error) {
		return &pluginapi.HTTPResponse{StatusCode: http.StatusBadGateway}, nil
	}
	defer func() { hostHTTP = orig }()

	out, err := pxpipeScopeToggle([]byte(`{"model":"grok-4.6","on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil || !env.OK {
		t.Fatalf("envelope: %s", out)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(resp.Body), "live_push_failed") {
		t.Fatalf("response = %+v %s", resp, resp.Body)
	}
}

func TestPxpipeTransformSkips(t *testing.T) {
	cfg := pluginConfig{PxpipeEnabled: true, PxpipeURL: "http://shim.test"}
	calls := 0
	orig := hostHTTP
	hostHTTP = func(req pluginapi.HTTPRequest) (*pluginapi.HTTPResponse, error) {
		calls++
		resp, _ := json.Marshal(map[string]any{
			"applied": true,
			"body":    map[string]any{"model": "x", "transformed": true},
		})
		return &pluginapi.HTTPResponse{StatusCode: 200, Body: resp}, nil
	}
	defer func() { hostHTTP = orig }()

	body := []byte(`{"model":"gpt-6-astra","messages":[]}`)

	marked := []byte(`{"model":"x","messages":[{"role":"system","content":"injected by pxpipe"}]}`)
	if got := pxpipeTransform(cfg, "openai", "gpt-6-astra", marked); string(got) != string(marked) {
		t.Fatal("marked body must bypass pxpipe")
	}

	got := pxpipeTransform(cfg, "openai", "gpt-6-astra", body)
	if !strings.Contains(string(got), "transformed") {
		t.Fatalf("expected transformed body, got %s", got)
	}
	if calls != 1 {
		t.Fatalf("shim calls = %d", calls)
	}

	cfg.PxpipeEnabled = false
	if got := pxpipeTransform(cfg, "openai", "gpt-6-astra", body); string(got) != string(body) {
		t.Fatal("disabled shim must pass through")
	}
}
