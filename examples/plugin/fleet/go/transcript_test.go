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

func TestTranscriptPreservesToolTurns(t *testing.T) {
	backend := &fakeBackend{
		live:   livePi,
		result: leadResult{Text: "done", Agent: "w1B:p1"},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	payload := `{"messages":[
		{"role":"user","content":"check then report"},
		{"role":"assistant","content":"checking","tool_calls":[{"id":"call_a1","type":"function","function":{"name":"read_file","arguments":"{\"p\":\"x\"}"}}]},
		{"role":"tool","tool_call_id":"call_a1","content":"file contents"},
		{"role":"user","content":"and now?"}]}`
	env := callMethod(t, pluginabi.MethodExecutorExecute, execRequest(virtualRouterModel, payload))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	task := backend.calls[0].Task
	for _, want := range []string{"check then report", "call_a1", "read_file", "file contents", "and now?"} {
		if !strings.Contains(task, want) {
			t.Fatalf("transcript missing %q:\n%s", want, task)
		}
	}
}

func TestPendingToolCallBlocksHandoff(t *testing.T) {
	backend := &fakeBackend{
		live:   livePi,
		result: leadResult{Text: "done", Agent: "w1B:p1"},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	payload := `{"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_pending","type":"function","function":{"name":"write_file","arguments":"{}"}}]}]}`
	env := callMethod(t, pluginabi.MethodExecutorExecute, execRequest(virtualRouterModel, payload))
	if env.OK || env.Error == nil || env.Error.Code != "unsafe_handoff" {
		t.Fatalf("env = %+v", env)
	}
	if env.Error.HTTPStatus != 409 || !strings.Contains(env.Error.Message, "call_pending") {
		t.Fatalf("error = %+v", env.Error)
	}
	if len(backend.calls) != 0 {
		t.Fatal("pending mutation reached the orchestrator")
	}
}

func TestLeadToolCallsSurfaceInCompletion(t *testing.T) {
	backend := &fakeBackend{
		live: livePi,
		result: leadResult{
			Text:  "",
			Agent: "w1B:p1",
			ToolCalls: []toolCall{
				{ID: "call_corr-1", Name: "apply_patch", Arguments: `{"diff":"x"}`},
			},
		},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"patch it"}]}`))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.Unmarshal(resp.Payload, &completion); err != nil {
		t.Fatal(err)
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	calls := choice["message"].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %v", calls)
	}
	c0 := calls[0].(map[string]any)
	if !strings.HasPrefix(c0["id"].(string), "call_") || c0["function"].(map[string]any)["name"] != "apply_patch" {
		t.Fatalf("tool_call = %v", c0)
	}
}

func TestCliHerdrParsesToolCallLines(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
		"agent list":   herdrAgentList,
		"agent prompt": `{"id":"x","result":{}}`,
		"agent read":   "CPA-RESULT-BEGIN-c1\nlet me patch\nCPA-TOOL-CALL:{\"name\":\"apply_patch\",\"arguments\":\"{\\\"d\\\":1}\"}\nCPA-VERIFY:ok\nCPA-RESULT-END-c1\n",
	})}
	res, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c1", Agent: "pi", Task: "x", Deadline: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "apply_patch" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if !strings.HasPrefix(res.ToolCalls[0].ID, "call_c1") {
		t.Fatalf("tool call id not correlation-scoped: %s", res.ToolCalls[0].ID)
	}
	if strings.Contains(res.Text, "CPA-TOOL-CALL") {
		t.Fatalf("marker leaked into text: %q", res.Text)
	}
}

// An uncertain outcome mid-escalation must surface and stop — the finalize
// turn that never ran proves nothing was replayed.
func TestUncertainOutcomeNeverReplays(t *testing.T) {
	backend := &fakeBackend{
		live:   twoLiveAgents,
		errAt:  2,
		err:    routerErr("outcome_uncertain", "wait bound exceeded; outcome unknown", 504),
		result: leadResult{Text: "x", Agent: "w1B:p1"},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"X-Fleet-Difficulty": {"high"}}
	env := callMethod(t, pluginabi.MethodExecutorExecute, req)
	if env.OK || env.Error == nil || env.Error.Code != "outcome_uncertain" {
		t.Fatalf("env = %+v", env)
	}
	if env.Error.Retryable {
		t.Fatal("uncertain outcome must not be retryable")
	}
	if len(backend.calls) != 2 {
		t.Fatalf("replay detected: %d calls", len(backend.calls))
	}
}
