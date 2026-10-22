package responses

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type responsesToChatState struct {
	id       string
	created  int64
	model    string
	roleSent bool
	tools    map[string]int
	nextTool int
}

// ConvertOpenAIResponsesResponseToOpenAIChatCompletions converts a Responses
// object or SSE event into a chat completion chunk. The executor feeds one
// SSE block at a time; non-SSE callers get the same JSON path.
func ConvertOpenAIResponsesResponseToOpenAIChatCompletions(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	if param == nil {
		return nil
	}
	state, _ := (*param).(*responsesToChatState)
	if state == nil {
		state = &responsesToChatState{tools: map[string]int{}}
		*param = state
	}
	event, root := responsesEvent(rawJSON)
	eventType := root.Get("type").String()
	if eventType == "" {
		eventType = event
	}
	switch eventType {
	case "response.created":
		response := root.Get("response")
		state.id = chatID(response.Get("id").String())
		state.created = response.Get("created_at").Int()
		state.model = firstNonEmpty(response.Get("model").String(), modelName)
		return [][]byte{state.chunk(map[string]any{"role": "assistant"}, nil, nil)}
	case "response.output_item.added":
		item := root.Get("item")
		if item.Get("type").String() != "function_call" {
			return nil
		}
		index := state.toolIndex(item.Get("id").String(), item.Get("call_id").String())
		chunk := state.chunk(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": index,
				"id":    item.Get("call_id").String(),
				"type":  "function",
				"function": map[string]any{
					"name":      item.Get("name").String(),
					"arguments": "",
				},
			}},
		}, nil, nil)
		return [][]byte{chunk}
	case "response.output_text.delta":
		delta := root.Get("delta").String()
		if delta == "" {
			return nil
		}
		return [][]byte{state.chunk(map[string]any{"content": delta}, nil, nil)}
	case "response.function_call_arguments.delta":
		index := state.toolIndex(root.Get("item_id").String(), root.Get("call_id").String())
		return [][]byte{state.chunk(map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    index,
				"function": map[string]any{"arguments": root.Get("delta").String()},
			}},
		}, nil, nil)}
	case "response.completed", "response.incomplete":
		response := root.Get("response")
		usage := responsesUsage(response.Get("usage"))
		finish := "stop"
		if state.nextTool > 0 {
			finish = "tool_calls"
		}
		if eventType == "response.incomplete" {
			finish = incompleteFinishReason(response.Get("incomplete_details.reason").String())
		}
		return [][]byte{state.chunk(map[string]any{}, finish, usage)}
	default:
		return nil
	}
}

func incompleteFinishReason(reason string) string {
	if reason == "content_filter" {
		return "content_filter"
	}
	return "length"
}

func (s *responsesToChatState) toolIndex(itemID, callID string) int {
	for _, key := range []string{itemID, callID} {
		if index, ok := s.tools[key]; key != "" && ok {
			return index
		}
	}
	index := s.nextTool
	s.nextTool++
	for _, key := range []string{itemID, callID} {
		if key != "" {
			s.tools[key] = index
		}
	}
	return index
}

func (s *responsesToChatState) chunk(delta map[string]any, finish any, usage map[string]any) []byte {
	id := s.id
	if id == "" {
		id = "chatcmpl-responses"
	}
	if s.created == 0 {
		s.created = time.Now().Unix()
	}
	if !s.roleSent {
		s.roleSent = true
		if _, ok := delta["role"]; !ok {
			delta["role"] = "assistant"
		}
	}
	body := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   firstNonEmpty(s.model, "unknown"),
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		body["usage"] = usage
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return encoded
}

// ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream converts a
// non-streaming Responses object into a chat completion object.
func ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream(_ context.Context, modelName string, _, _, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)
	response := root
	if root.Get("response").Exists() {
		response = root.Get("response")
	}
	text, refusal, toolCalls := responsesOutput(response.Get("output"))
	if text == "" {
		if outputText := response.Get("output_text"); outputText.Exists() {
			text = outputText.String()
		}
	}
	message := map[string]any{"role": "assistant", "content": text}
	if refusal != "" {
		message["refusal"] = refusal
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if text == "" {
			message["content"] = nil
		}
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	if response.Get("status").String() == "incomplete" {
		finish = incompleteFinishReason(response.Get("incomplete_details.reason").String())
	}
	created := response.Get("created_at").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	body := map[string]any{
		"id":      chatID(firstNonEmpty(response.Get("id").String(), "resp_local")),
		"object":  "chat.completion",
		"created": created,
		"model":   firstNonEmpty(response.Get("model").String(), modelName, "unknown"),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if usage := responsesUsage(response.Get("usage")); usage != nil {
		body["usage"] = usage
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return rawJSON
	}
	return encoded
}

func responsesOutput(output gjson.Result) (string, string, []any) {
	var text strings.Builder
	var refusal string
	var toolCalls []any
	for _, item := range output.Array() {
		switch item.Get("type").String() {
		case "message":
			for _, part := range item.Get("content").Array() {
				switch part.Get("type").String() {
				case "output_text", "text":
					text.WriteString(part.Get("text").String())
				case "refusal":
					refusal = part.Get("refusal").String()
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, map[string]any{
				"id":   item.Get("call_id").String(),
				"type": "function",
				"function": map[string]any{
					"name":      item.Get("name").String(),
					"arguments": item.Get("arguments").String(),
				},
			})
		}
	}
	return text.String(), refusal, toolCalls
}

func responsesUsage(usage gjson.Result) map[string]any {
	if !usage.Exists() {
		return nil
	}
	prompt := usage.Get("input_tokens").Int()
	completion := usage.Get("output_tokens").Int()
	total := usage.Get("total_tokens").Int()
	if total == 0 {
		total = prompt + completion
	}
	out := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if cached := usage.Get("input_tokens_details.cached_tokens").Int(); cached > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if reasoning := usage.Get("output_tokens_details.reasoning_tokens").Int(); reasoning > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoning}
	}
	return out
}

func chatID(id string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "chatcmpl-") {
		return id
	}
	return "chatcmpl-" + strings.TrimPrefix(id, "resp_")
}

func responsesEvent(raw []byte) (string, gjson.Result) {
	event := ""
	var data []byte
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			data = []byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
	}
	if len(data) == 0 {
		data = []byte(strings.TrimSpace(string(raw)))
	}
	if !gjson.ValidBytes(data) {
		return "", gjson.Result{}
	}
	return event, gjson.ParseBytes(data)
}
