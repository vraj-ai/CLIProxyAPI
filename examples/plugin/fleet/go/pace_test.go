package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestUsagePace(t *testing.T) {
	resetState(pluginConfig{})
	markRunning("or/grok-4.6")
	running := currentPace()
	if running.RunningModel != "or/grok-4.6" {
		t.Fatalf("running model = %q", running.RunningModel)
	}
	if running.TokensPerSec != 0 {
		t.Fatalf("running request invented a rate: %v", running.TokensPerSec)
	}

	rec := pluginapi.UsageRecord{
		Model:   "or/grok-4.6",
		APIKey:  "sk-live-secret-should-not-leak",
		Latency: 2 * time.Second,
		Detail:  pluginapi.UsageDetail{OutputTokens: 80, InputTokens: 10},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noteUsage(raw); err != nil {
		t.Fatal(err)
	}
	got := currentPace()
	if got.RunningModel != "" {
		t.Fatalf("completion left the model running: %+v", got)
	}
	if got.Model != "or/grok-4.6" || got.OutputTokens != 80 || got.TokensPerSec != 40 {
		t.Fatalf("pace = %+v, want 40 tok/s from 80 tokens in 2s", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-live-secret") || strings.Contains(string(encoded), "APIKey") {
		t.Fatalf("pace view leaked the usage credential: %s", encoded)
	}
}
