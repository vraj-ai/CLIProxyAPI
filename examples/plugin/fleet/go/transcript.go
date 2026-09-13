// transcript.go: client-visible context and tool-call correlation across
// task/subtask/turn transitions. The lead sees the whole conversation, not a
// flattened last message; unresolved tool calls are pending mutations that
// block rerouting; a lead may answer with tool calls that keep correlation
// ids traceable back to this request.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const toolCallMarker = "CPA-TOOL-CALL:"

type toolCall struct {
	ID        string
	Name      string
	Arguments string
}

// conversationText serializes the openai message list into the transcript
// the lead executes. It is the context-preservation half of ticket #13: prior
// turns, tool calls, and tool results all reach the orchestration boundary
// intact. It fails closed on unanswered tool calls — a pending mutation
// means some prior owner still holds the write side of the turn, and
// rerouting now would hand the next agent an unsafe boundary.
func conversationText(payload []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil || doc == nil {
		return "", routerErr("bad_request", "executor payload is not a JSON object", http.StatusBadRequest)
	}
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return "", routerErr("bad_request", "executor payload has no messages array", http.StatusBadRequest)
	}
	var b strings.Builder
	pending := map[string]bool{}
	answered := map[string]bool{}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		switch role {
		case "tool":
			id, _ := mm["tool_call_id"].(string)
			answered[id] = true
			fmt.Fprintf(&b, "\n[tool %s]\n%s", id, messageText(mm))
		case "assistant":
			if t := messageText(mm); t != "" {
				fmt.Fprintf(&b, "\n[assistant]\n%s", t)
			}
			for _, tc := range toolCallList(mm) {
				pending[tc.ID] = true
				fmt.Fprintf(&b, "\n[assistant tool_call %s]\n%s(%s)", tc.ID, tc.Name, tc.Arguments)
			}
		default:
			if t := messageText(mm); t != "" {
				fmt.Fprintf(&b, "\n[%s]\n%s", role, t)
			}
		}
	}
	for id := range pending {
		if !answered[id] {
			return "", routerErr("unsafe_handoff",
				"conversation contains unanswered tool_call "+id+"; its owner must resolve it before rerouting — never auto-replayed",
				http.StatusConflict)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", routerErr("bad_request", "executor payload has no message text", http.StatusBadRequest)
	}
	return out, nil
}

// messageText extracts content from one openai message (string or parts).
func messageText(m map[string]any) string {
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func toolCallList(m map[string]any) []toolCall {
	raw, ok := m["tool_calls"].([]any)
	if !ok {
		return nil
	}
	var out []toolCall
	for _, item := range raw {
		tm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tm["function"].(map[string]any)
		name, _ := fn["name"].(string)
		args := ""
		switch a := fn["arguments"].(type) {
		case string:
			args = a
		default:
			if raw, err := json.Marshal(a); err == nil {
				args = string(raw)
			}
		}
		id, _ := tm["id"].(string)
		out = append(out, toolCall{ID: id, Name: name, Arguments: args})
	}
	return out
}

// splitToolCalls peels CPA-TOOL-CALL:<json> lines from a framed answer and
// assigns correlation-scoped ids so the client can match responses to this
// request's routing decision.
func splitToolCalls(text, corr string) (string, []toolCall) {
	var calls []toolCall
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(l)
		if !strings.HasPrefix(trim, toolCallMarker) {
			kept = append(kept, l)
			continue
		}
		var spec struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(trim, toolCallMarker)), &spec) != nil || spec.Name == "" {
			kept = append(kept, l)
			continue
		}
		args, _ := spec.Arguments.(string)
		if args == "" && spec.Arguments != nil {
			if raw, err := json.Marshal(spec.Arguments); err == nil {
				args = string(raw)
			}
		}
		calls = append(calls, toolCall{
			ID:        fmt.Sprintf("call_%s-%d", corr, len(calls)+1),
			Name:      spec.Name,
			Arguments: args,
		})
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), calls
}
