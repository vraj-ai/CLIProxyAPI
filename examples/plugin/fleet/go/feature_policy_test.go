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

func TestModelFeaturePolicyInheritsAndOverrides(t *testing.T) {
	resetState(pluginConfig{Caveman: true, Ponytail: true, PxpipeEnabled: true})
	falseValue := false
	state.mu.Lock()
	state.policies = map[string]modelFeaturePolicy{
		"or/grok-4.6": {Ponytail: &falseValue},
	}
	state.mu.Unlock()

	if !modelFeatureEnabled(state.config, "or/grok-4.6", featureCaveman) {
		t.Fatal("model did not inherit caveman default")
	}
	if modelFeatureEnabled(state.config, "or/grok-4.6", featurePonytail) {
		t.Fatal("model override did not disable ponytail")
	}
	if !modelFeatureEnabled(state.config, "or/grok-4.6", featurePxpipe) {
		t.Fatal("model did not inherit pxpipe default")
	}
}

func TestInterceptUsesExactModelInstructionPolicy(t *testing.T) {
	resetState(pluginConfig{Caveman: true, Ponytail: true})
	falseValue := false
	state.mu.Lock()
	state.policies = map[string]modelFeaturePolicy{"or/grok-4.6": {Ponytail: &falseValue}}
	state.mu.Unlock()
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "or/grok-4.6",
		Body:         []byte(`{"model":"or/grok-4.6","messages":[{"role":"user","content":"hi"}]}`),
	})
	if !strings.Contains(string(resp.Body), cavemanText) || strings.Contains(string(resp.Body), ponytailText) {
		t.Fatalf("policy was not applied to request body: %s", resp.Body)
	}
}

func TestSetModelFeaturePersistsAndResets(t *testing.T) {
	resetState(pluginConfig{Caveman: true, Ponytail: true})
	path := filepath.Join(t.TempDir(), "fleet-policies.json")
	state.mu.Lock()
	state.policyPath = path
	state.mu.Unlock()

	out, err := setModelFeature([]byte(`{"model":"or/grok-4.6","feature":"ponytail","value":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("set envelope: %v %s", err, out)
	}
	var saved featurePolicyFile
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Policies["or/grok-4.6"].Ponytail == nil || *saved.Policies["or/grok-4.6"].Ponytail {
		t.Fatalf("saved policy = %+v", saved)
	}

	if _, err := setModelFeature([]byte(`{"model":"or/grok-4.6","feature":"ponytail","value":null}`)); err != nil {
		t.Fatal(err)
	}
	policies, err := loadModelPolicies(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 0 {
		t.Fatalf("reset left policies = %+v", policies)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("policy mode = %o", info.Mode().Perm())
	}
}

func TestSetModelFeatureRejectsInvalidInput(t *testing.T) {
	resetState(pluginConfig{})
	for _, raw := range []string{
		`{"model":"or/grok-4.6","feature":"nope","value":true}`,
		`{"model":"or/grok-4.6","feature":"ponytail","value":"yes"}`,
	} {
		out, err := setModelFeature([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		if json.Unmarshal(out, &env) != nil || !env.OK {
			t.Fatalf("expected structured rejection for %s", raw)
		}
		var resp struct {
			StatusCode int
		}
		if err := json.Unmarshal(env.Result, &resp); err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("response = %s", out)
		}
	}
}
