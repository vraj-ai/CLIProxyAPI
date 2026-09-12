package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const testText = "fleet test"

func TestInject(t *testing.T) {
	claudeWithSystem := `{"model":"claude-x","system":"be brief","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"bash"}]}`
	claudeNoSystem := `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`
	openaiString := `{"model":"gpt-x","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`
	openaiArray := `{"model":"gpt-x","messages":[{"role":"system","content":[{"type":"text","text":"be brief"}]},{"role":"user","content":"hi"}]}`
	responses := `{"model":"gpt-x","instructions":"be brief","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"},"tools":[{"type":"web_search"}]}`

	tests := []struct {
		name        string
		format      string
		body        string
		wantChanged bool
		check       func(t *testing.T, doc map[string]any)
	}{
		{
			name:        "claude messages and string system",
			format:      "claude",
			body:        claudeWithSystem,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				if doc["system"] != "be brief\n\n"+testText {
					t.Fatalf("system = %v", doc["system"])
				}
				msgs := doc["messages"].([]any)
				if msgs[0].(map[string]any)["content"] != "hi" {
					t.Fatalf("messages clobbered: %v", msgs)
				}
				if doc["tools"] == nil {
					t.Fatal("tools dropped")
				}
			},
		},
		{
			name:        "claude messages without system",
			format:      "claude",
			body:        claudeNoSystem,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				if doc["system"] != testText {
					t.Fatalf("system = %v", doc["system"])
				}
				if doc["messages"].([]any)[0].(map[string]any)["content"] != "hi" {
					t.Fatal("messages clobbered")
				}
			},
		},
		{
			name:        "openai string system",
			format:      "openai",
			body:        openaiString,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				msgs := doc["messages"].([]any)
				if msgs[0].(map[string]any)["content"] != "be brief\n\n"+testText {
					t.Fatalf("content = %v", msgs[0])
				}
				if msgs[1].(map[string]any)["content"] != "hi" {
					t.Fatal("user message clobbered")
				}
			},
		},
		{
			name:        "openai array system content",
			format:      "openai",
			body:        openaiArray,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				c := doc["messages"].([]any)[0].(map[string]any)["content"].([]any)
				if len(c) != 2 || c[0].(map[string]any)["text"] != testText || c[1].(map[string]any)["text"] != "be brief" {
					t.Fatalf("content = %v", c)
				}
			},
		},
		{
			name:        "responses instructions and input",
			format:      "openai-response",
			body:        responses,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				if doc["instructions"] != testText+"\n\nbe brief" {
					t.Fatalf("instructions = %v", doc["instructions"])
				}
				if doc["input"].([]any)[0].(map[string]any)["content"] != "hi" {
					t.Fatal("input clobbered")
				}
				if doc["reasoning"].(map[string]any)["effort"] != "high" {
					t.Fatal("reasoning dropped")
				}
				if doc["tools"] == nil {
					t.Fatal("tools dropped")
				}
			},
		},
		{
			name:        "codex instructions and input",
			format:      "codex",
			body:        responses,
			wantChanged: true,
			check: func(t *testing.T, doc map[string]any) {
				if doc["instructions"] != testText+"\n\nbe brief" {
					t.Fatalf("instructions = %v", doc["instructions"])
				}
			},
		},
		{name: "malformed json", format: "openai", body: `{"messages":`, wantChanged: false},
		{name: "null json", format: "openai", body: `null`, wantChanged: false},
		{name: "empty body", format: "openai", body: ``, wantChanged: false},
		{name: "unknown format", format: "gemini", body: claudeWithSystem, wantChanged: false},
		{name: "openai invalid content", format: "openai", body: `{"messages":[{"role":"system","content":42}]}`, wantChanged: false},
		{name: "anthropic invalid system", format: "claude", body: `{"system":42,"messages":[]}`, wantChanged: false},
		{name: "responses invalid instructions", format: "openai-response", body: `{"instructions":42,"input":[]}`, wantChanged: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := inject(testText, tc.format, []byte(tc.body))
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v (out=%s)", changed, tc.wantChanged, out)
			}
			if !tc.wantChanged {
				if !bytes.Equal(out, []byte(tc.body)) {
					t.Fatalf("body mutated on no-change: %s", out)
				}
				return
			}
			var doc map[string]any
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output not json: %v", err)
			}
			tc.check(t, doc)
		})
	}
}

func runIntercept(t *testing.T, method string, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	envRaw, err := handleMethod(method, raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(envRaw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}
	var resp pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func resetState(cfg pluginConfig) {
	state.mu.Lock()
	state.config = cfg
	state.injections = 0
	state.mu.Unlock()
}

func TestInterceptBeforeAfter(t *testing.T) {
	resetState(pluginConfig{Caveman: true, Ponytail: true})
	body := []byte(`{"model":"claude-x","system":"be brief","messages":[{"role":"user","content":"hi"}]}`)
	req := pluginapi.RequestInterceptRequest{
		SourceFormat: "claude",
		Model:        "claude-x",
		Headers:      http.Header{"x-a": {"1"}},
		Body:         body,
	}
	before := runIntercept(t, pluginabi.MethodRequestInterceptBefore, req)
	if bytes.Equal(before.Body, body) {
		t.Fatal("before did not inject")
	}
	after := runIntercept(t, pluginabi.MethodRequestInterceptAfter, pluginapi.RequestInterceptRequest{
		SourceFormat: "claude",
		Model:        "claude-x",
		Headers:      before.Headers,
		Body:         before.Body,
	})
	if !bytes.Equal(after.Body, before.Body) {
		t.Fatal("after body differs from before body")
	}
	state.mu.Lock()
	inj := state.injections
	state.mu.Unlock()
	if inj != 1 {
		t.Fatalf("injections = %d, want 1", inj)
	}
}

func TestInterceptFlagsOff(t *testing.T) {
	resetState(pluginConfig{Caveman: false, Ponytail: false})
	body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`)
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Model:        "gpt-x",
		Body:         body,
	})
	if !bytes.Equal(resp.Body, body) {
		t.Fatalf("body changed with flags off: %s", resp.Body)
	}
}

func TestInterceptPrefixExcluded(t *testing.T) {
	resetState(pluginConfig{Caveman: true, Ponytail: true, Models: []string{"gpt-"}})
	body := []byte(`{"model":"claude-x","system":"s","messages":[{"role":"user","content":"hi"}]}`)
	resp := runIntercept(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
		SourceFormat:   "claude",
		Model:          "claude-x",
		RequestedModel: "claude-x",
		Body:           body,
	})
	if !bytes.Equal(resp.Body, body) {
		t.Fatalf("body changed for excluded model: %s", resp.Body)
	}
}

func TestSavingsHTMLEscapesModel(t *testing.T) {
	px := pxStats{ByModel: map[string]int64{"<script>alert(1)</script>": 10}}
	html := savingsHTML(px, ugStats{}, 0)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("model name not escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("escaped model name missing")
	}
	if !strings.Contains(html, `<iframe src="http://127.0.0.1:47821/"></iframe>`) {
		t.Fatal("iframe changed")
	}
}

func runSavingsJSON(t *testing.T) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/savings",
		Query:  url.Values{"format": {"json"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	envRaw, err := handleMethod("management.handle", raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(envRaw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		return nil, fmt.Errorf("envelope error: %+v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload, nil
}

func TestSavingsMeasurements(t *testing.T) {
	dir := t.TempDir()
	pxPath := filepath.Join(dir, "events.jsonl")
	ugPath := filepath.Join(dir, "usage-gain.jsonl")
	px := `{"method":"POST","model":"test","compressed":true,"status":200,"orig_chars":100,"outgoing_text_chars":60}
{"method":"POST","model":"test","compressed":true,"status":401,"orig_chars":100,"outgoing_text_chars":0}
{"method":"POST","model":"test","compressed":true,"status":200,"orig_chars":100}
{"method":"POST","model":"test","compressed":true,"status":200,"orig_chars":100,"outgoing_text_chars":150}
not-json
`
	ug := `{"source":"compression","originalBytes":100,"outputBytes":60}
{"source":"other","originalBytes":100,"outputBytes":0}
{"source":"compression","originalBytes":100}
`
	if err := os.WriteFile(pxPath, []byte(px), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ugPath, []byte(ug), 0o600); err != nil {
		t.Fatal(err)
	}
	resetState(pluginConfig{PxpipeEvents: pxPath, UsageGain: ugPath})

	payload, err := runSavingsJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	pxs := payload["pxpipe"].(map[string]any)
	if pxs["requests"] != float64(4) {
		t.Fatalf("requests = %v", pxs["requests"])
	}
	if pxs["compressed"] != float64(2) {
		t.Fatalf("compressed = %v", pxs["compressed"])
	}
	if pxs["chars_before"] != float64(200) {
		t.Fatalf("chars_before = %v", pxs["chars_before"])
	}
	if pxs["chars_after"] != float64(210) {
		t.Fatalf("chars_after = %v", pxs["chars_after"])
	}
	if pxs["saved_pct"] != float64(-5) {
		t.Fatalf("saved_pct = %v", pxs["saved_pct"])
	}
	if pxs["saved_chars_by_model"].(map[string]any)["test"] != float64(-10) {
		t.Fatalf("by model test = %v", pxs["saved_chars_by_model"])
	}
	ugs := payload["subagent_context"].(map[string]any)
	if ugs["events"] != float64(1) {
		t.Fatalf("ug events = %v", ugs["events"])
	}
	if ugs["bytes_before"] != float64(100) {
		t.Fatalf("ug bytes_before = %v", ugs["bytes_before"])
	}
	if ugs["bytes_after"] != float64(60) {
		t.Fatalf("ug bytes_after = %v", ugs["bytes_after"])
	}
	if ugs["saved_pct"] != float64(40) {
		t.Fatalf("ug saved_pct = %v", ugs["saved_pct"])
	}
	note, _ := payload["note"].(string)
	if !strings.Contains(note, "not attributed") || !strings.Contains(note, "not measure token or dollar") {
		t.Fatalf("note = %q", note)
	}

	resetState(pluginConfig{
		PxpipeEvents: filepath.Join(dir, "missing-events.jsonl"),
		UsageGain:    ugPath,
	})
	if _, err := runSavingsJSON(t); err == nil {
		t.Fatal("missing telemetry file did not error")
	}

	big := bytes.Repeat([]byte("x"), 1<<20+1)
	bigPath := filepath.Join(dir, "big-events.jsonl")
	if err := os.WriteFile(bigPath, append(big, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	resetState(pluginConfig{PxpipeEvents: bigPath, UsageGain: ugPath})
	if _, err := runSavingsJSON(t); err == nil {
		t.Fatal("oversized scanner line did not error")
	}
}

func TestRegistrationModelsFieldIsArray(t *testing.T) {
	reg := buildRegistration()
	var models *pluginapi.ConfigField
	for i, f := range reg.Metadata.ConfigFields {
		if f.Name == "models" {
			models = &reg.Metadata.ConfigFields[i]
		}
	}
	if models == nil {
		t.Fatal("models config field missing")
	}
	if models.Type != pluginapi.ConfigFieldTypeArray {
		t.Fatalf("models type = %q, want array", models.Type)
	}
	if models.Description != "Model prefixes to inject; empty means all." {
		t.Fatalf("models description = %q", models.Description)
	}
}
