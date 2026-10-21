package responses

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ConvertOpenAIChatCompletionsRequestToOpenAIResponses converts a chat
// completions request into a Responses request. It is the forward half of the
// chat-client to Responses-upstream pair; the reverse half lives in
// openai_responses_to_chat.go.
func ConvertOpenAIChatCompletionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := map[string]any{
		"model":  firstNonEmpty(modelName, root.Get("model").String()),
		"stream": stream,
	}
	if instructions := chatInstructions(root.Get("messages")); instructions != "" {
		out["instructions"] = instructions
	}
	if input := chatInput(root.Get("messages")); len(input) > 0 {
		out["input"] = input
	}
	if tools := chatTools(root.Get("tools")); len(tools) > 0 {
		out["tools"] = tools
	}
	if choice, ok := chatToolChoice(root.Get("tool_choice")); ok {
		out["tool_choice"] = choice
	}
	if max := firstInt(root, "max_completion_tokens", "max_tokens"); max > 0 {
		out["max_output_tokens"] = max
	}
	if v := root.Get("temperature"); v.Exists() {
		out["temperature"] = v.Float()
	}
	if v := root.Get("top_p"); v.Exists() {
		out["top_p"] = v.Float()
	}
	if v := root.Get("parallel_tool_calls"); v.Exists() {
		out["parallel_tool_calls"] = v.Bool()
	}
	out["store"] = root.Get("store").Bool()
	if v := root.Get("metadata"); v.Exists() {
		out["metadata"] = v.Value()
	}
	if v := root.Get("service_tier"); v.Exists() {
		out["service_tier"] = v.String()
	}
	if v := root.Get("prompt_cache_key"); v.Exists() {
		out["prompt_cache_key"] = v.String()
	}
	if effort := root.Get("reasoning_effort").String(); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort}
	}
	if format, ok := chatResponseFormat(root.Get("response_format")); ok {
		out["text"] = map[string]any{"format": format}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return inputRawJSON
	}
	return body
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstInt(root gjson.Result, keys ...string) int64 {
	for _, key := range keys {
		if value := root.Get(key); value.Exists() {
			return value.Int()
		}
	}
	return 0
}

func chatInstructions(messages gjson.Result) string {
	var parts []string
	for _, message := range messages.Array() {
		role := message.Get("role").String()
		if role != "system" && role != "developer" {
			continue
		}
		if text := contentText(message.Get("content")); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func chatInput(messages gjson.Result) []any {
	input := make([]any, 0, len(messages.Array()))
	for _, message := range messages.Array() {
		switch message.Get("role").String() {
		case "system", "developer":
			continue
		case "user":
			input = append(input, map[string]any{
				"role":    "user",
				"content": chatUserContent(message.Get("content")),
			})
		case "assistant":
			text := contentText(message.Get("content"))
			if text != "" {
				input = append(input, map[string]any{
					"role":    "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			for _, call := range message.Get("tool_calls").Array() {
				fn := call.Get("function")
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.Get("id").String(),
					"name":      fn.Get("name").String(),
					"arguments": stringOrJSON(fn.Get("arguments")),
				})
			}
		case "tool", "function":
			callID := message.Get("tool_call_id").String()
			if callID == "" {
				callID = message.Get("name").String()
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  contentText(message.Get("content")),
			})
		}
	}
	return input
}

func chatUserContent(content gjson.Result) any {
	if !content.IsArray() {
		return contentText(content)
	}
	items := make([]any, 0, len(content.Array()))
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text":
			items = append(items, map[string]any{"type": "input_text", "text": part.Get("text").String()})
		case "image_url", "input_image":
			url := part.Get("image_url.url").String()
			if url == "" {
				url = part.Get("image_url").String()
			}
			if url == "" {
				url = part.Get("url").String()
			}
			if url != "" {
				items = append(items, map[string]any{"type": "input_image", "image_url": url})
			}
		case "file", "input_file":
			items = append(items, part.Value())
		}
	}
	return items
}

func contentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, part := range content.Array() {
		if text := part.Get("text"); text.Exists() {
			parts = append(parts, text.String())
			continue
		}
		if part.Type == gjson.String {
			parts = append(parts, part.String())
		}
	}
	return strings.Join(parts, "")
}

func stringOrJSON(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	return value.Raw
}

func chatTools(tools gjson.Result) []any {
	if !tools.IsArray() {
		return nil
	}
	out := make([]any, 0, len(tools.Array()))
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" || !tool.Get("function").Exists() {
			out = append(out, tool.Value())
			continue
		}
		fn := tool.Get("function")
		item := map[string]any{
			"type":       "function",
			"name":       fn.Get("name").String(),
			"parameters": fn.Get("parameters").Value(),
		}
		if description := fn.Get("description").String(); description != "" {
			item["description"] = description
		}
		if strict := fn.Get("strict"); strict.Exists() {
			item["strict"] = strict.Bool()
		}
		out = append(out, item)
	}
	return out
}

func chatToolChoice(choice gjson.Result) (any, bool) {
	if !choice.Exists() {
		return nil, false
	}
	if choice.Type == gjson.String {
		return choice.String(), true
	}
	if name := choice.Get("function.name").String(); name != "" {
		return map[string]any{"type": "function", "name": name}, true
	}
	return choice.Value(), true
}

func chatResponseFormat(format gjson.Result) (any, bool) {
	if !format.Exists() {
		return nil, false
	}
	switch format.Get("type").String() {
	case "json_object":
		return map[string]any{"type": "json_object"}, true
	case "json_schema":
		schema := format.Get("json_schema")
		item := map[string]any{"type": "json_schema", "schema": schema.Get("schema").Value()}
		if name := schema.Get("name").String(); name != "" {
			item["name"] = name
		}
		if strict := schema.Get("strict"); strict.Exists() {
			item["strict"] = strict.Bool()
		}
		return item, true
	default:
		return format.Value(), true
	}
}
