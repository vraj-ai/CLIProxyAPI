// stream.go: bounded streaming for the virtual model. The synchronous-chunks
// response mode is the smallest contract that preserves order and explicit
// termination: the host plays the chunk slice in order, then closes. The
// whole orchestration completes — or fails — before any chunk exists, so a
// partial stream cannot leave this executor; failure is the error envelope,
// never a truncated chunk list. Client cancellation is bounded by the
// request deadline: cgo plugin calls carry no mid-call cancel signal, so
// every backend turn runs on the shared ctx and cannot spawn once it is done.
package main

import (
	"encoding/json"
	"net/http"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// executorStreamResponse mirrors the host's rpcExecutorStreamResponse wire
// shape: headers plus an ordered chunk slice.
type executorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

func executorExecuteStream(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorEnvelope("bad_request", "executor stream request did not parse: "+err.Error()), nil
	}
	payload, err := routeToLead(&req)
	if err != nil {
		return routerErrorEnvelope(err), nil
	}
	chunks, err := openaiStreamChunks(payload)
	if err != nil {
		return errorEnvelope("plugin_error", err.Error()), nil
	}
	return okEnvelope(executorStreamResponse{
		Headers: http.Header{"content-type": {"text/event-stream"}},
		Chunks:  chunks,
	})
}

// openaiStreamChunks converts the completed chat.completion into the standard
// OpenAI SSE event order: role delta, content/tool-call delta with
// finish_reason, then the explicit [DONE] sentinel.
func openaiStreamChunks(completion []byte) ([]pluginapi.ExecutorStreamChunk, error) {
	var doc map[string]any
	if err := json.Unmarshal(completion, &doc); err != nil {
		return nil, err
	}
	choices, _ := doc["choices"].([]any)
	if len(choices) == 0 {
		return nil, routerErr("plugin_error", "completion has no choices", http.StatusInternalServerError)
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)

	roleRaw, _ := json.Marshal(streamChunkDoc(doc, map[string]any{"role": "assistant"}, nil))
	delta := map[string]any{}
	if c, ok := msg["content"].(string); ok && c != "" {
		delta["content"] = c
	}
	if tc, ok := msg["tool_calls"]; ok {
		delta["tool_calls"] = tc
	}
	finish, _ := choice["finish_reason"].(string)
	deltaRaw, _ := json.Marshal(streamChunkDoc(doc, delta, finish))
	return []pluginapi.ExecutorStreamChunk{
		{Payload: append([]byte("data: "), append(roleRaw, '\n', '\n')...)},
		{Payload: append([]byte("data: "), append(deltaRaw, '\n', '\n')...)},
		{Payload: []byte("data: [DONE]\n\n")},
	}, nil
}

func streamChunkDoc(completion map[string]any, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      completion["id"],
		"object":  "chat.completion.chunk",
		"created": completion["created"],
		"model":   completion["model"],
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
}
