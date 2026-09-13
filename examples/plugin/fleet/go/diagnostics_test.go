package main

import (
	"encoding/json"
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func routerResource(t *testing.T) map[string]any {
	t.Helper()
	env := callMethod(t, "management.handle",
		pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/fleet/router"})
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRouterDiagnosticsEvidence(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live:   livePi,
		result: leadResult{Text: "ok", Agent: "w1B:p1"},
	})
	callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))

	doc := routerResource(t)
	decisions, ok := doc["decisions"].([]any)
	if !ok || len(decisions) != 1 {
		t.Fatalf("decisions = %v", doc["decisions"])
	}
	d := decisions[0].(map[string]any)
	for _, field := range []string{"correlation_id", "outcome", "model", "effort", "quota_freshness"} {
		if d[field] == nil || d[field] == "" {
			t.Fatalf("decision missing %s: %v", field, d)
		}
	}
	if d["outcome"] != "lead_completed" || d["model"] != "gpt-6-astra" || d["effort"] != "low" {
		t.Fatalf("decision = %v", d)
	}
	if d["quota_freshness"] != "fresh" {
		t.Fatalf("quota_freshness = %v", d["quota_freshness"])
	}

	readiness, ok := doc["readiness"].(map[string]any)
	if !ok {
		t.Fatalf("readiness = %v", doc["readiness"])
	}
	if readiness["model_registered"] != true || readiness["executor_identifier"] != routerProvider {
		t.Fatalf("readiness = %v", readiness)
	}
	excluded := readiness["policy_excluded"].([]any)
	if len(excluded) == 0 || excluded[0] != "opus 5 medium" {
		t.Fatalf("policy_excluded = %v", excluded)
	}
	if scope, _ := readiness["scope"].(string); !strings.Contains(scope, "not live inference") {
		t.Fatalf("scope = %v", readiness["scope"])
	}
	// Prompt text must never appear in diagnostics.
	if strings.Contains(string(mustJSON(doc)), "hi\"") {
		t.Fatal("diagnostics contain prompt text")
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func TestRouterDiagnosticsRedactSentinels(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live:   livePi,
		err:    routerErr("lead_failed", "upstream said key=sk-sentinel-aaaa-bbbb and flag FLEET-SENTINEL-99", 502),
		result: leadResult{Text: "x"},
	})
	state.mu.Lock()
	state.sentinels = []string{"FLEET-SENTINEL-99"}
	state.mu.Unlock()

	// Seed a decision whose free-text reason carries both a secret-shaped
	// token and a configured sentinel — record-time scrubbing plus the
	// output pass must strip both.
	recordDecision(routeDecision{
		CorrelationID: "corr-secret", At: "2026-09-13T00:00:00Z",
		Model: "cpa router", Outcome: "failed",
		Reason: "upstream leaked sk-sentinel-aaaa-bbbb alongside FLEET-SENTINEL-99",
	})
	callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	env := callMethod(t, "management.handle",
		pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/fleet/router"})
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	body := string(resp.Body)
	if strings.Contains(body, "sk-sentinel-aaaa-bbbb") || strings.Contains(body, "FLEET-SENTINEL-99") {
		t.Fatalf("diagnostics leaked a secret: %s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Fatalf("redaction marker missing: %s", body)
	}
}

func TestRouterResourceDoesNotServeWhenConcreteBypass(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live:   livePi,
		result: leadResult{Text: "ok"},
	})
	// Concrete-model regression: bypass decision is recorded as unsupported,
	// and diagnostics still report it without reaching orchestration.
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest("gpt-6-astra", `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK {
		t.Fatal("concrete model must not route through the executor")
	}
	doc := routerResource(t)
	if d, ok := doc["decisions"].([]any); ok && len(d) != 0 {
		t.Fatal("bypassed request must not record an orchestration decision")
	}
}
