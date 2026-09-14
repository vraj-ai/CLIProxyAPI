package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementDispatchHubPage(t *testing.T) {
	resetRouter(pluginConfig{}, nil)
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/fleet/hub",
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
	if ct := resp.Headers.Get("content-type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(string(resp.Body), "Router") || !strings.Contains(string(resp.Body), "Quota") {
		t.Fatalf("hub page missing panels")
	}
	if strings.Contains(string(resp.Body), `"eligible"`) && strings.Contains(string(resp.Body), `"decisions"`) && strings.Contains(string(resp.Body), "correlation_id") {
		// hub shell must not embed live router JSON
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

func TestResourceRouterIsHTMLShell(t *testing.T) {
	out, err := managementDispatch(&pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/fleet/router",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope: %s", out)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(resp.Headers.Get("content-type")), "text/html") {
		t.Fatalf("content-type = %q", resp.Headers.Get("content-type"))
	}
	if strings.Contains(string(resp.Body), `"readiness"`) {
		t.Fatal("resource /router leaked diagnostics JSON")
	}
}

func TestManagementDispatchState(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, nil)
	state.mu.Lock()
	state.config.RouterEnabled = true
	state.mu.Unlock()
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
	if !st.Flags.Router || !st.Router.Enabled {
		t.Fatalf("router flag not reflected: %+v", st.Flags)
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

	if got := pxpipeTransform(cfg, "openai", virtualRouterModel, body); string(got) != string(body) {
		t.Fatal("router model must bypass pxpipe")
	}
	if calls != 0 {
		t.Fatal("router bypass still hit the shim")
	}

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
