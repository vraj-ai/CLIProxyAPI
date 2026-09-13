package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// The routing-contract suite treats the plugin as a black box: calls enter
// through handleMethod exactly as the host invokes them over the ABI, and the
// Herdr boundary is faked. No live service or inference is exercised.

type fakeBackend struct {
	result leadResult
	err    error
	live   []herdrAgent
	calls  []leadRequest
	queue  []leadResult // per-turn results; falls back to result
	errAt  int          // fail only the Nth call (1-based), 0 = all via err
}

func (f *fakeBackend) agents(_ context.Context) ([]herdrAgent, error) {
	return f.live, nil
}

func (f *fakeBackend) runLead(_ context.Context, req leadRequest) (leadResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil && (f.errAt == 0 || f.errAt == len(f.calls)) {
		return leadResult{}, f.err
	}
	if len(f.queue) > 0 {
		r := f.queue[0]
		f.queue = f.queue[1:]
		return r, nil
	}
	return f.result, nil
}

func resetRouter(cfg pluginConfig, b leadBackend) {
	resetState(cfg)
	state.mu.Lock()
	state.backend = b
	state.quota = fakeQuota{snap: healthyQuota()}
	state.decisions = nil
	state.mu.Unlock()
}

func callMethod(t *testing.T, method string, request any) envelope {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleMethod(method, raw)
	if err != nil {
		t.Fatalf("handleMethod(%s) error: %v", method, err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("response is not an envelope: %v", err)
	}
	return env
}

func execRequest(model string, payload string) executorCallRequest {
	return executorCallRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:        model,
			SourceFormat: "openai",
			Format:       "openai",
			Payload:      []byte(payload),
		},
	}
}

func TestModelRegisterGatesOnRouterEnabled(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: false}, &fakeBackend{})
	env := callMethod(t, pluginabi.MethodModelRegister, map[string]any{})
	if !env.OK {
		t.Fatal("model.register failed")
	}
	var resp pluginapi.ModelRegistrationResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Provider != routerProvider {
		t.Fatalf("provider = %q", resp.Provider)
	}
	if len(resp.Models) != 0 {
		t.Fatalf("disabled router advertised models: %v", resp.Models)
	}

	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, &fakeBackend{})
	env = callMethod(t, pluginabi.MethodModelRegister, map[string]any{})
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 || resp.Models[0].ID != virtualRouterModel {
		t.Fatalf("models = %+v", resp.Models)
	}
}

func TestExecutorIdentifier(t *testing.T) {
	resetRouter(pluginConfig{}, &fakeBackend{})
	env := callMethod(t, pluginabi.MethodExecutorIdentifier, map[string]any{})
	if !env.OK {
		t.Fatal("executor.identifier failed")
	}
	var resp struct {
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Identifier != routerProvider {
		t.Fatalf("identifier = %q", resp.Identifier)
	}
}

func TestExecutorExecuteReturnsLeadResponse(t *testing.T) {
	backend := &fakeBackend{result: leadResult{Text: "lead answer", Agent: "w1B:p1"}, live: []herdrAgent{{Name: "pi", Status: "idle", PaneID: "w1B:p1"}}}
	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, backend)

	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"system","content":"ctx"},{"role":"user","content":"say hi"}]}`))
	if !env.OK {
		t.Fatalf("execute failed: %+v", env.Error)
	}
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.Unmarshal(resp.Payload, &completion); err != nil {
		t.Fatalf("payload is not a completion: %v", err)
	}
	if completion["object"] != "chat.completion" || completion["model"] != virtualRouterModel {
		t.Fatalf("completion = %v", completion)
	}
	msg := completion["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "lead answer" || msg["role"] != "assistant" {
		t.Fatalf("message = %v", msg)
	}
	if len(backend.calls) != 1 {
		t.Fatalf("lead calls = %d", len(backend.calls))
	}
	call := backend.calls[0]
	if !strings.Contains(call.Task, "say hi") || call.Agent != "w1B:p1" || call.CorrelationID == "" {
		t.Fatalf("lead request = %+v", call)
	}
	if completion["id"] != "chatcmpl-"+call.CorrelationID {
		t.Fatalf("completion id %v does not carry correlation %s", completion["id"], call.CorrelationID)
	}
}

func TestExecutorExecuteConcreteModelBypassed(t *testing.T) {
	backend := &fakeBackend{result: leadResult{Text: "x"}, live: []herdrAgent{{Name: "pi", Status: "idle", PaneID: "w1B:p1"}}}
	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, backend)
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest("gpt-6-astra", `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error == nil || env.Error.Code != "unsupported_model" {
		t.Fatalf("env = %+v", env)
	}
	if len(backend.calls) != 0 {
		t.Fatal("concrete model reached the lead backend")
	}
}

func TestExecutorExecuteDisabledRouter(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: false}, &fakeBackend{})
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error.Code != "router_disabled" {
		t.Fatalf("env = %+v", env)
	}
}

func TestExecutorUnsupportedCapabilitiesFailExplicitly(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, &fakeBackend{})
	for _, method := range []string{
		pluginabi.MethodExecutorExecuteStream,
		pluginabi.MethodExecutorCountTokens,
		pluginabi.MethodExecutorHTTPRequest,
	} {
		env := callMethod(t, method, map[string]any{})
		if env.OK || env.Error == nil || env.Error.Code != "unsupported_capability" {
			t.Fatalf("%s: env = %+v", method, env)
		}
	}
}

func TestExecutorExecuteLeadFailureSurfaces(t *testing.T) {
	backend := &fakeBackend{err: routerErr("lead_busy", "lead is working", 409), live: []herdrAgent{{Name: "pi", Status: "idle", PaneID: "w1B:p1"}}}
	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, backend)
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error == nil || env.Error.Code != "lead_busy" {
		t.Fatalf("env = %+v", env)
	}
	if env.Error.HTTPStatus != 409 {
		t.Fatalf("http_status = %d", env.Error.HTTPStatus)
	}
}

func TestConversationTextShapes(t *testing.T) {
	got, err := conversationText([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`))
	if err != nil || !strings.Contains(got, "a\nb") {
		t.Fatalf("array content: %q %v", got, err)
	}
	if _, err := conversationText([]byte(`{"messages":[{"role":"assistant","content":"hi"}]}`)); err != nil {
		t.Fatalf("assistant-only conversation should serialize: %v", err)
	}
	if _, err := conversationText([]byte(`{"input":[]}`)); err == nil {
		t.Fatal("missing messages did not error")
	}
}

func TestExtractMarked(t *testing.T) {
	corr := "cpa-1-0001"
	text := "prompt echo mentions CPA-RESULT-BEGIN-" + corr + " inside instructions\nreal output\nCPA-RESULT-BEGIN-" + corr + "\nthe answer\nCPA-RESULT-END-" + corr + "\n"
	got, ok := extractMarked(text, corr)
	if !ok || got != "the answer" {
		t.Fatalf("extractMarked = %q %v", got, ok)
	}
	if _, ok := extractMarked("no markers at all", corr); ok {
		t.Fatal("missing markers reported ok")
	}
	if _, ok := extractMarked("CPA-RESULT-BEGIN-"+corr+"\n\nCPA-RESULT-END-"+corr, corr); ok {
		t.Fatal("empty answer reported ok")
	}
	if _, ok := extractMarked("CPA-RESULT-END-other", corr); ok {
		t.Fatal("foreign correlation marker reported ok")
	}
}

// --- cliHerdr against a fake command runner ---

func runnerReturning(outs map[string]string) commandRunner {
	return func(_ context.Context, argv ...string) ([]byte, error) {
		key := strings.Join(argv[1:], " ")
		for match, out := range outs {
			if strings.Contains(key, match) {
				return []byte(out), nil
			}
		}
		return nil, fmt.Errorf("unexpected argv: %s", key)
	}
}

const herdrAgentList = `{"id":"cli:agent:list","result":{"agents":[{"agent":"pi","agent_status":"idle","pane_id":"w1B:p1","cwd":"/work"},{"agent":"codex","agent_status":"working","pane_id":"w1B:p9","cwd":"/work"}],"type":"agent_list"}}`

func TestCliHerdrRunLeadHappyPath(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
		"agent list":   herdrAgentList,
		"agent prompt": `{"id":"cli:agent:prompt","result":{"state":"idle"},"type":"agent_prompt"}`,
		"agent read":   "noise\nCPA-RESULT-BEGIN-corr1\nfinal text\nCPA-RESULT-END-corr1\ntrail",
	})}
	res, err := h.runLead(context.Background(), leadRequest{CorrelationID: "corr1", Agent: "pi", Task: "do it", Deadline: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "final text" || res.Agent != "w1B:p1" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCliHerdrLeadBusy(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{"agent list": herdrAgentList})}
	_, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c", Agent: "codex", Task: "x", Deadline: time.Second})
	re, ok := err.(*routerError)
	if !ok || re.code != "lead_busy" {
		t.Fatalf("err = %v", err)
	}
}

func TestCliHerdrLeadUnavailable(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{"agent list": herdrAgentList})}
	_, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c", Agent: "grok", Task: "x", Deadline: time.Second})
	re, ok := err.(*routerError)
	if !ok || re.code != "lead_unavailable" {
		t.Fatalf("err = %v", err)
	}
}

func TestCliHerdrPromptErrorMapping(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{`{"id":"x","error":{"code":"agent_blocked","message":"b"}}`, "lead_blocked"},
		{`{"id":"x","error":{"code":"agent_prompt_stalled","message":"s"}}`, "lead_stalled"},
		{`{"id":"x","error":{"code":"timeout","message":"t"}}`, "outcome_uncertain"},
	}
	for _, tc := range cases {
		h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
			"agent list":   herdrAgentList,
			"agent prompt": tc.out,
		})}
		_, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c", Agent: "pi", Task: "x", Deadline: time.Second})
		re, ok := err.(*routerError)
		if !ok || re.code != tc.want {
			t.Fatalf("%s: err = %v", tc.out, err)
		}
	}
}

func TestCliHerdrUnverifiedResult(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
		"agent list":   herdrAgentList,
		"agent prompt": `{"id":"cli:agent:prompt","result":{},"type":"agent_prompt"}`,
		"agent read":   "output without the marker for this request",
	})}
	_, err := h.runLead(context.Background(), leadRequest{CorrelationID: "corr9", Agent: "pi", Task: "x", Deadline: time.Second})
	re, ok := err.(*routerError)
	if !ok || re.code != "lead_result_unverified" {
		t.Fatalf("err = %v", err)
	}
}

func TestCliHerdrEnvelopeError(t *testing.T) {
	h := &cliHerdr{bin: "herdr", run: runnerReturning(map[string]string{
		"agent list": `{"id":"cli:agent:list","error":{"code":"api_down","message":"socket closed"}}`,
	})}
	_, err := h.runLead(context.Background(), leadRequest{CorrelationID: "c", Agent: "pi", Task: "x", Deadline: time.Second})
	re, ok := err.(*routerError)
	if !ok || re.code != "herdr_api_down" {
		t.Fatalf("err = %v", err)
	}
}

func TestDecisionRecorded(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true, RouterAgent: "pi"}, &fakeBackend{
		result: leadResult{Text: "ok", Agent: "w1B:p1"},
		live:   []herdrAgent{{Name: "pi", Status: "idle", PaneID: "w1B:p1"}},
	})
	callMethod(t, pluginabi.MethodExecutorExecute, execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	callMethod(t, pluginabi.MethodExecutorExecute, execRequest("gpt-6-astra", `{"messages":[{"role":"user","content":"hi"}]}`))
	state.mu.Lock()
	defer state.mu.Unlock()
	var okCount int
	for _, d := range state.decisions {
		if d.Outcome == "lead_completed" {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("completed decisions = %d", okCount)
	}
}
