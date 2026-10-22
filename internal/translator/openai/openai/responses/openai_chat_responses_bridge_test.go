package responses

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestChatRequestToResponsesPreservesImageAndTools(t *testing.T) {
	request := []byte(`{
		"model":"muse-spark-1.3-contributor",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":[
				{"type":"text","text":"what is this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
			]},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"max_tokens":64,
		"stream":true,
		"stream_options":{"include_usage":true}
	}`)
	out := ConvertOpenAIChatCompletionsRequestToOpenAIResponses("muse-spark-1.3-contributor", request, true)
	root := gjson.ParseBytes(out)
	if root.Get("instructions").String() != "be brief" {
		t.Fatalf("instructions = %q", root.Get("instructions").String())
	}
	if root.Get("max_output_tokens").Int() != 64 {
		t.Fatalf("max_output_tokens = %d", root.Get("max_output_tokens").Int())
	}
	if root.Get("stream").Bool() != true {
		t.Fatal("stream not set")
	}
	if root.Get("stream_options").Exists() {
		t.Fatal("stream_options leaked")
	}
	input := root.Get("input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d: %s", len(input), out)
	}
	if input[0].Get("content.1.type").String() != "input_image" ||
		input[0].Get("content.1.image_url").String() != "data:image/png;base64,AAAA" {
		t.Fatalf("image part lost: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call" || input[1].Get("call_id").String() != "call_1" {
		t.Fatalf("tool call lost: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "function_call_output" || input[2].Get("output").String() != "result" {
		t.Fatalf("tool result lost: %s", input[2].Raw)
	}
	if root.Get("tools.0.name").String() != "lookup" || root.Get("tools.0.type").String() != "function" {
		t.Fatalf("tool definition lost: %s", root.Get("tools").Raw)
	}
	if root.Get("tool_choice.name").String() != "lookup" {
		t.Fatalf("tool choice lost: %s", root.Get("tool_choice").Raw)
	}
}

func TestResponsesNonStreamToChat(t *testing.T) {
	response := []byte(`{
		"id":"resp_1","object":"response","created_at":1700000000,"model":"muse",
		"status":"completed",
		"output":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}
		],
		"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
	}`)
	out := ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream(context.Background(), "muse", nil, nil, response, nil)
	root := gjson.ParseBytes(out)
	if root.Get("choices.0.message.tool_calls.0.id").String() != "call_1" ||
		root.Get("choices.0.message.tool_calls.0.function.name").String() != "lookup" {
		t.Fatalf("tool call lost: %s", out)
	}
	if root.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish = %s", root.Get("choices.0.finish_reason").String())
	}
	if root.Get("usage.prompt_tokens").Int() != 10 || root.Get("usage.completion_tokens").Int() != 5 {
		t.Fatalf("usage = %s", root.Get("usage").Raw)
	}
}

func TestResponsesStreamToChat(t *testing.T) {
	var param any
	call := func(block string) [][]byte {
		return ConvertOpenAIResponsesResponseToOpenAIChatCompletions(context.Background(), "muse", nil, nil, []byte(block), &param)
	}
	created := call("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1700000000,\"model\":\"muse\"}}\n\n")
	if len(created) != 1 || gjson.GetBytes(created[0], "choices.0.delta.role").String() != "assistant" {
		t.Fatalf("role chunk = %v", created)
	}
	delta := call("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	if len(delta) != 1 || gjson.GetBytes(delta[0], "choices.0.delta.content").String() != "hi" {
		t.Fatalf("content chunk = %v", delta)
	}
	final := call("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
	if len(final) != 1 {
		t.Fatalf("final = %v", final)
	}
	if gjson.GetBytes(final[0], "choices.0.finish_reason").String() != "stop" ||
		gjson.GetBytes(final[0], "usage.prompt_tokens").Int() != 2 {
		t.Fatalf("final chunk = %s", final[0])
	}
	var doc map[string]any
	if err := json.Unmarshal(final[0], &doc); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesStreamToolCall(t *testing.T) {
	var param any
	call := func(block string) [][]byte {
		return ConvertOpenAIResponsesResponseToOpenAIChatCompletions(context.Background(), "muse", nil, nil, []byte(block), &param)
	}
	call("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	added := call("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\"}}\n\n")
	if len(added) != 1 || gjson.GetBytes(added[0], "choices.0.delta.tool_calls.0.function.name").String() != "lookup" {
		t.Fatalf("tool header = %v", added)
	}
	args := call("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\"}\n\n")
	if len(args) != 1 || gjson.GetBytes(args[0], "choices.0.delta.tool_calls.0.function.arguments").String() != `{"q":` {
		t.Fatalf("tool args = %v", args)
	}
	final := call("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	if gjson.GetBytes(final[0], "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish = %s", final[0])
	}
}

func TestChatRequestToResponsesDefaultsStoreFalse(t *testing.T) {
	out := ConvertOpenAIChatCompletionsRequestToOpenAIResponses("m", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), false)
	if v := gjson.GetBytes(out, "store"); !v.Exists() || v.Bool() {
		t.Fatalf("store = %s, want false", v.Raw)
	}
	out = ConvertOpenAIChatCompletionsRequestToOpenAIResponses("m", []byte(`{"store":true,"messages":[{"role":"user","content":"hi"}]}`), false)
	if !gjson.GetBytes(out, "store").Bool() {
		t.Fatal("explicit store=true must be kept")
	}
}

func TestResponsesStreamWithoutCreatedStillSendsRoleOnce(t *testing.T) {
	var param any
	ctx := context.Background()
	first := ConvertOpenAIResponsesResponseToOpenAIChatCompletions(ctx, "m", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"a"}`), &param)
	second := ConvertOpenAIResponsesResponseToOpenAIChatCompletions(ctx, "m", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"b"}`), &param)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("chunks = %d, %d", len(first), len(second))
	}
	if got := gjson.GetBytes(first[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("first delta role = %q", got)
	}
	if gjson.GetBytes(second[0], "choices.0.delta.role").Exists() {
		t.Fatalf("role repeated: %s", second[0])
	}
	if gjson.GetBytes(first[0], "created").Int() != gjson.GetBytes(second[0], "created").Int() {
		t.Fatal("created drifted between chunks")
	}
}

func TestResponsesStreamToolDeltaKeyedByCallIDReusesIndex(t *testing.T) {
	var param any
	ctx := context.Background()
	ConvertOpenAIResponsesResponseToOpenAIChatCompletions(ctx, "m", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`), &param)
	delta := ConvertOpenAIResponsesResponseToOpenAIChatCompletions(ctx, "m", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{}"}`), &param)
	if len(delta) != 1 || gjson.GetBytes(delta[0], "choices.0.delta.tool_calls.0.index").Int() != 0 {
		t.Fatalf("delta = %s", delta)
	}
}

func TestResponsesStreamIgnoresHeaderOnlyLines(t *testing.T) {
	var param any
	out := ConvertOpenAIResponsesResponseToOpenAIChatCompletions(context.Background(), "m", nil, nil, []byte("event: response.completed"), &param)
	if len(out) != 0 {
		t.Fatalf("header-only line produced %s", out)
	}
}

func TestResponsesIncompleteFinishReasons(t *testing.T) {
	var param any
	out := ConvertOpenAIResponsesResponseToOpenAIChatCompletions(context.Background(), "m", nil, nil,
		[]byte(`data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`), &param)
	if len(out) != 1 || gjson.GetBytes(out[0], "choices.0.finish_reason").String() != "content_filter" {
		t.Fatalf("stream = %s", out)
	}
	body := ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream(context.Background(), "m", nil, nil,
		[]byte(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`), &param)
	if gjson.GetBytes(body, "choices.0.finish_reason").String() != "length" {
		t.Fatalf("non-stream = %s", body)
	}
}

func TestResponsesNonStreamKeepsTextAlongsideToolCalls(t *testing.T) {
	body := ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream(context.Background(), "m", nil, nil,
		[]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]}`), nil)
	if gjson.GetBytes(body, "choices.0.message.content").String() != "hello" ||
		gjson.GetBytes(body, "choices.0.message.tool_calls.0.id").String() != "c1" {
		t.Fatalf("body = %s", body)
	}
}
