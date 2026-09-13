package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type streamResult struct {
	Headers map[string][]string          `json:"headers"`
	Chunks  []map[string]json.RawMessage `json:"chunks"`
}

func streamChunksOf(t *testing.T, env envelope) streamResult {
	t.Helper()
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	var res streamResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func payloadText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b []byte
	if err := json.Unmarshal(raw, &b); err != nil {
		// Payload may arrive as a string field on the chunk object.
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 != nil {
			t.Fatalf("chunk payload: %v / %v", err, err2)
		}
		b = []byte(s)
	}
	return string(b)
}

func TestStreamOrderedAndTerminated(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live:   livePi,
		result: leadResult{Text: "streamed answer", Agent: "w1B:p1"},
	})
	env := callMethod(t, pluginabi.MethodExecutorExecuteStream,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	res := streamChunksOf(t, env)
	if ct := res.Headers["content-type"]; len(ct) == 0 || ct[0] != "text/event-stream" {
		t.Fatalf("headers = %v", res.Headers)
	}
	if len(res.Chunks) != 3 {
		t.Fatalf("chunks = %d", len(res.Chunks))
	}
	var p0, p1 string
	for i, c := range res.Chunks {
		raw, ok := c["Payload"]
		if !ok {
			t.Fatalf("chunk %d has no Payload: %v", i, c)
		}
		txt := payloadText(t, raw)
		if i == 0 {
			p0 = txt
		}
		if i == 1 {
			p1 = txt
		}
	}
	if !strings.HasPrefix(p0, "data: ") || !strings.Contains(p0, `"role":"assistant"`) {
		t.Fatalf("chunk0 = %q", p0)
	}
	if !strings.Contains(p1, `"content":"streamed answer"`) || !strings.Contains(p1, `"finish_reason":"stop"`) {
		t.Fatalf("chunk1 = %q", p1)
	}
	last := payloadText(t, res.Chunks[2]["Payload"])
	if last != "data: [DONE]\n\n" {
		t.Fatalf("terminator = %q", last)
	}
}

func TestStreamToolCallsInDelta(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live: livePi,
		result: leadResult{
			Agent: "w1B:p1",
			ToolCalls: []toolCall{
				{ID: "call_x", Name: "apply_patch", Arguments: "{}"},
			},
		},
	})
	env := callMethod(t, pluginabi.MethodExecutorExecuteStream,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"go"}]}`))
	res := streamChunksOf(t, env)
	delta := payloadText(t, res.Chunks[1]["Payload"])
	if !strings.Contains(delta, "apply_patch") || !strings.Contains(delta, `"finish_reason":"tool_calls"`) {
		t.Fatalf("delta = %q", delta)
	}
}

func TestStreamFailureIsTerminalNotPartial(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live: livePi,
		err:  routerErr("lead_busy", "lead working", 409),
	})
	env := callMethod(t, pluginabi.MethodExecutorExecuteStream,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK {
		t.Fatal("failure must return an error envelope, never a partial chunk list")
	}
	if env.Error == nil || env.Error.Code != "lead_busy" {
		t.Fatalf("env = %+v", env)
	}
}

func TestStreamUncertainOutcomeBounded(t *testing.T) {
	resetRouter(pluginConfig{RouterEnabled: true}, &fakeBackend{
		live: livePi,
		err:  routerErr("outcome_uncertain", "wait bound exceeded", 504),
	})
	env := callMethod(t, pluginabi.MethodExecutorExecuteStream,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error == nil || env.Error.Code != "outcome_uncertain" || env.Error.Retryable {
		t.Fatalf("env = %+v", env)
	}
}

// A canceled context must stop orchestration dead: no pane prompt is issued
// and no mutation can start once the caller is gone.
func TestCanceledContextStopsWork(t *testing.T) {
	var prompted bool
	h := &cliHerdr{bin: "herdr", run: func(ctx context.Context, argv ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(strings.Join(argv, " "), "agent prompt") {
			prompted = true
		}
		return []byte(herdrAgentList), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.runLead(ctx, leadRequest{CorrelationID: "c1", Agent: "pi", Task: "x"})
	if err == nil {
		t.Fatal("canceled context must fail")
	}
	if prompted {
		t.Fatal("a prompt was issued after cancellation")
	}
}
