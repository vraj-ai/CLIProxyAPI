package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestResponsesUsageDetail_IgnoresNullUsage(t *testing.T) {
	if got := responsesUsageDetail([]byte(`{"type":"response.created","response":{"usage":null}}`)); got != nil {
		t.Fatalf("null usage should yield no detail, got %+v", *got)
	}
	got := responsesUsageDetail([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":3}}}`))
	if got == nil || got.InputTokens != 7 || got.OutputTokens != 3 || got.TotalTokens != 10 {
		t.Fatalf("unexpected detail %+v", got)
	}
}

func TestOpenAICompatExecutor_ResponsesStreamReportsFinalUsage(t *testing.T) {
	const alias = "openai-compat-responses-stream-usage"
	stream := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"r1","model":"resp-model","usage":null}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"r1","model":"resp-model","usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}` + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:   "compat",
			Models: []config.OpenAICompatibilityModel{{Name: "resp-model", Alias: "resp-model", Protocol: "responses"}},
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL,
			"api_key":      "test-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	res, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "resp-model",
		Payload: []byte(`{"model":"resp-model","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream ended without its terminal event being honoured: %v", chunk.Err)
		}
	}
	record := capture.await(t)
	if record.Detail.InputTokens != 7 || record.Detail.OutputTokens != 3 || record.Detail.TotalTokens != 10 {
		t.Fatalf("recorded usage = %+v, want input 7 output 3 total 10", record.Detail)
	}
}
