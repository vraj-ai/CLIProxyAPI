package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// twoLiveAgents covers the common escalation shape: astra low leads, swe 2 max
// is the first independent candidate.
var twoLiveAgents = []herdrAgent{
	{Name: "pi", Status: "idle", PaneID: "w1B:p1"},
	{Name: "devin", Status: "idle", PaneID: "w1B:p5"},
}

func escalatedBackend() *fakeBackend {
	return &fakeBackend{
		live: twoLiveAgents,
		queue: []leadResult{
			{Text: "draft answer", Agent: "w1B:p1", Verification: "ok"},
			{Text: "draft misses the retry path", Agent: "w1B:p5"},
			{Text: "final answer", Agent: "w1B:p1", Verification: "ok"},
		},
	}
}

func TestRoutineRequestStaysSingleLead(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, escalatedBackend())
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	backend := currentBackend().(*fakeBackend)
	if len(backend.calls) != 1 || backend.calls[0].Role != roleLead {
		t.Fatalf("calls = %+v", backend.calls)
	}
	state.mu.Lock()
	d := state.decisions[len(state.decisions)-1]
	state.mu.Unlock()
	if d.Escalation != "" || d.Sidekick != "" || d.Outcome != "lead_completed" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDifficultyTriggerEscalates(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, escalatedBackend())
	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"x-fleet-difficulty": {"high"}}
	env := callMethod(t, pluginabi.MethodExecutorExecute, req)
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	backend := currentBackend().(*fakeBackend)
	if len(backend.calls) != 3 {
		t.Fatalf("calls = %+v", backend.calls)
	}
	lead, side, fin := backend.calls[0], backend.calls[1], backend.calls[2]
	if lead.Role != roleLead || lead.Agent != "w1B:p1" {
		t.Fatalf("lead call = %+v", lead)
	}
	if side.Role != roleSidekick || side.Agent != "w1B:p5" || side.Agent == lead.Agent {
		t.Fatalf("sidekick call = %+v", side)
	}
	if !strings.Contains(side.Task, "draft answer") || !strings.Contains(side.Task, "Independently review") {
		t.Fatalf("sidekick task = %q", side.Task)
	}
	if fin.Role != roleFinalize || fin.Agent != "w1B:p1" {
		t.Fatalf("finalize call = %+v", fin)
	}
	if !strings.Contains(fin.Task, "draft misses the retry path") {
		t.Fatalf("finalize task = %q", fin.Task)
	}
	// The client-visible text is the lead's finalize turn, never the sidekick's.
	state.mu.Lock()
	d := state.decisions[len(state.decisions)-1]
	state.mu.Unlock()
	if d.Outcome != "escalated_completed" || d.Escalation != "difficulty" || d.Sidekick != "swe 2 max@w1B:p5" {
		t.Fatalf("decision = %+v", d)
	}
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.Unmarshal(resp.Payload, &completion); err != nil {
		t.Fatal(err)
	}
	content := completion["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "final answer" {
		t.Fatalf("client content = %v", content)
	}
}

func TestImportanceTriggerEscalates(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, escalatedBackend())
	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"x-fleet-importance": {"critical"}}
	env := callMethod(t, pluginabi.MethodExecutorExecute, req)
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	backend := currentBackend().(*fakeBackend)
	if len(backend.calls) != 3 || backend.calls[1].Role != roleSidekick {
		t.Fatalf("calls = %+v", backend.calls)
	}
}

func TestFailedVerificationEscalates(t *testing.T) {
	backend := &fakeBackend{
		live: twoLiveAgents,
		queue: []leadResult{
			{Text: "draft answer", Agent: "w1B:p1", Verification: "failed"},
			{Text: "found the gap", Agent: "w1B:p5"},
			{Text: "corrected answer", Agent: "w1B:p1", Verification: "ok"},
		},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	if len(backend.calls) != 3 {
		t.Fatalf("calls = %+v", backend.calls)
	}
	state.mu.Lock()
	d := state.decisions[len(state.decisions)-1]
	state.mu.Unlock()
	if d.Escalation != "verification_failed" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestNoEligibleSidekickFailsClosed(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live:   livePi, // only the lead's provider is live
		result: leadResult{Text: "draft", Agent: "w1B:p1", Verification: "ok"},
	})
	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"x-fleet-difficulty": {"high"}}
	env := callMethod(t, pluginabi.MethodExecutorExecute, req)
	if env.OK || env.Error == nil || env.Error.Code != "no_eligible_sidekick" {
		t.Fatalf("env = %+v", env)
	}
	if env.Error.HTTPStatus != 503 {
		t.Fatalf("status = %d", env.Error.HTTPStatus)
	}
	backend := currentBackend().(*fakeBackend)
	if len(backend.calls) != 1 {
		t.Fatal("sidekick/finalize ran without an eligible candidate")
	}
}

func TestSidekickSelectionRespectsQuota(t *testing.T) {
	freezeNow(t)
	quota := healthyQuota()
	paced := freshProviderAt(80, testNow)
	paced.Resources["weekly"] = quotaResource{Kind: "consumption", Remaining: 2, Limit: 100,
		ResetsAt: testNow.Add(20 * time.Hour), WindowSeconds: 604800, Unit: "percent"}
	quota["devin"] = paced // swe 2 max paced out as sidekick
	backend := &fakeBackend{
		live: []herdrAgent{
			{Name: "pi", Status: "idle", PaneID: "w1B:p1"},
			{Name: "devin", Status: "idle", PaneID: "w1B:p5"},
			{Name: "grok", Status: "idle", PaneID: "w2:p3"},
		},
		queue: []leadResult{
			{Text: "draft", Agent: "w1B:p1"},
			{Text: "review", Agent: "w2:p3"},
			{Text: "final", Agent: "w1B:p1"},
		},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	state.mu.Lock()
	state.quota = fakeQuota{snap: quota}
	state.mu.Unlock()
	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"x-fleet-difficulty": {"high"}}
	env := callMethod(t, pluginabi.MethodExecutorExecute, req)
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	if backend.calls[1].Agent != "w2:p3" || backend.calls[1].Model != "grok-4.6" {
		t.Fatalf("sidekick call = %+v", backend.calls[1])
	}
}

// The adapter must give the sidekick no mutation channel: the wire prompt
// carries the read-only directive and the role, and nothing the sidekick
// returns reaches the client except through the lead's finalize turn.
func TestCliHerdrSidekickPromptIsReadOnly(t *testing.T) {
	var prompts []string
	h := &cliHerdr{bin: "herdr", run: func(_ context.Context, argv ...string) ([]byte, error) {
		key := strings.Join(argv[1:], " ")
		switch {
		case strings.Contains(key, "agent list"):
			return []byte(herdrAgentList), nil
		case strings.Contains(key, "agent prompt"):
			prompts = append(prompts, argv[4])
			return []byte(`{"id":"x","result":{}}`), nil
		default:
			return []byte("CPA-RESULT-BEGIN-c1\nok\nCPA-VERIFY:ok\nCPA-RESULT-END-c1\n"), nil
		}
	}}
	_, err := h.runLead(context.Background(), leadRequest{
		CorrelationID: "c1", Agent: "pi", Role: roleSidekick, Task: "review this", Deadline: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "READ-ONLY") || !strings.Contains(prompts[0], "role: sidekick") {
		t.Fatalf("prompt = %q", prompts)
	}
}

func TestCliHerdrVerificationParsing(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
		"agent list":   herdrAgentList,
		"agent prompt": `{"id":"x","result":{}}`,
		"agent read":   "noise\nCPA-RESULT-BEGIN-c1\nsome text\nCPA-VERIFY:failed\nCPA-RESULT-END-c1\n",
	})}
	res, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c1", Agent: "pi", Task: "x", Deadline: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verification != "failed" || res.Text != "some text" {
		t.Fatalf("res = %+v", res)
	}
}

func TestSplitVerification(t *testing.T) {
	if text, v := splitVerification("answer\nCPA-VERIFY:ok"); text != "answer" || v != "ok" {
		t.Fatalf("%q %q", text, v)
	}
	if text, v := splitVerification("answer"); text != "answer" || v != "" {
		t.Fatalf("%q %q", text, v)
	}
	if text, v := splitVerification("answer\nCPA-VERIFY:garbage"); text != "answer" || v != "" {
		t.Fatalf("%q %q", text, v)
	}
}
