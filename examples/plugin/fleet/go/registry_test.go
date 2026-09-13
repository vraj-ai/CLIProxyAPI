package main

import (
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var livePi = []herdrAgent{{Name: "pi", Status: "idle", PaneID: "w1B:p1"}}

func TestSelectFirstEligible(t *testing.T) {
	sel, err := selectCandidate(livePi, "", healthyQuota(), testNow, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Chosen.Label != "astra low" || sel.Effort != "low" {
		t.Fatalf("selection = %+v", sel)
	}
	if sel.Agent.PaneID != "w1B:p1" {
		t.Fatalf("agent = %+v", sel.Agent)
	}
}

func TestSelectSkipsIneligibleInOrder(t *testing.T) {
	// Only grok live: astra unavailable, swe unavailable, opus policy-excluded,
	// grok chosen, muse never reached.
	live := []herdrAgent{{Name: "grok", Status: "idle", PaneID: "w2:p3"}}
	sel, err := selectCandidate(live, "", healthyQuota(), testNow, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Chosen.Kind != "grok" || sel.Agent.PaneID != "w2:p3" {
		t.Fatalf("selection = %+v", sel)
	}
	want := map[string]string{
		"astra low":     "agent_unavailable",
		"swe 2 max":     "agent_unavailable",
		"opus 5 medium": "policy_excluded",
	}
	for _, s := range sel.Skipped {
		if want[s.Label] != "" && s.Reason != want[s.Label] {
			t.Fatalf("%s skipped with %q, want %q", s.Label, s.Reason, want[s.Label])
		}
	}
	if len(sel.Skipped) != 3 {
		t.Fatalf("skipped = %+v", sel.Skipped)
	}
}

func TestClaudeNeverEligible(t *testing.T) {
	// Even when claude is the only live agent, policy excludes it.
	live := []herdrAgent{{Name: "claude", Status: "idle", PaneID: "w9:p1"}}
	_, err := selectCandidate(live, "", healthyQuota(), testNow, false, "")
	if err == nil {
		t.Fatal("claude-only selection did not fail")
	}
	re, ok := err.(*routerError)
	if !ok || re.code != "no_eligible_candidate" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(re.message, "opus 5 medium: policy_excluded") {
		t.Fatalf("message = %q", re.message)
	}
}

func TestRequestedEffortFilters(t *testing.T) {
	// astra verifies low..max; "high" forwards exactly.
	sel, err := selectCandidate(livePi, "high", healthyQuota(), testNow, false, "")
	if err != nil || sel.Effort != "high" {
		t.Fatalf("selection = %+v %v", sel, err)
	}
	// "turbo" is unsupported for astra and unverified for opaque candidates.
	_, err = selectCandidate(livePi, "turbo", healthyQuota(), testNow, false, "")
	re, ok := err.(*routerError)
	if !ok || re.code != "no_eligible_candidate" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(re.message, "astra low: effort_unsupported") {
		t.Fatalf("message = %q", re.message)
	}
}

func TestAllIneligibleFailsClearly(t *testing.T) {
	_, err := selectCandidate(nil, "", healthyQuota(), testNow, false, "")
	re, ok := err.(*routerError)
	if !ok || re.code != "no_eligible_candidate" {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"agent_unavailable", "policy_excluded", "subscription_unverified"} {
		if !strings.Contains(re.message, want) {
			t.Fatalf("message %q missing %q", re.message, want)
		}
	}
}

func TestRegistryEvidenceComplete(t *testing.T) {
	for _, c := range preferenceOrder {
		if c.Evidence == "" {
			t.Fatalf("%s has no evidence", c.Label)
		}
	}
	var claude candidate
	for _, c := range preferenceOrder {
		if c.Kind == "claude" {
			claude = c
		}
	}
	if claude.PolicyOK {
		t.Fatal("claude candidate is policy-eligible; exclusion must be preserved")
	}
}

func TestExecuteSelectsCandidateEndToEnd(t *testing.T) {
	backend := &fakeBackend{
		result: leadResult{Text: "ok", Agent: "w2:p3"},
		live:   []herdrAgent{{Name: "grok", Status: "idle", PaneID: "w2:p3"}},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	call := backend.calls[0]
	if call.Agent != "w2:p3" || call.Model != "grok-4.6" || call.Effort != "high" {
		t.Fatalf("lead request = %+v", call)
	}
	_ = pluginapi.ExecutorRequest{}
}
