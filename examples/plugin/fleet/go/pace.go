package main

import (
	"encoding/json"
	"strings"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hubPace is the latest provider completion the plugin has seen, plus the
// model of a request that has started and not yet produced a usage record.
// TokensPerSec is output tokens divided by latency. A running request has
// no rate.
type hubPace struct {
	Model        string  `json:"model,omitempty"`
	TokensPerSec float64 `json:"tokens_per_sec,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	LatencyMS    int64   `json:"latency_ms,omitempty"`
	At           string  `json:"at,omitempty"`
	RunningModel string  `json:"running_model,omitempty"`
}

type paceState struct {
	model        string
	tokensPerSec float64
	outputTokens int64
	latency      time.Duration
	at           time.Time
	runningModel string
	runningSince time.Time
}

func markRunning(model string) {
	model = strings.TrimSpace(model)
	if model == "" || model == policyAll {
		return
	}
	state.mu.Lock()
	state.pace.runningModel = model
	state.pace.runningSince = time.Now()
	state.mu.Unlock()
}

func noteUsage(raw []byte) ([]byte, error) {
	var rec pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return okEnvelopeJSON(`{}`)
	}
	// The usage record carries the client API key. It never enters pace state.
	rec.APIKey = ""
	model := strings.TrimSpace(rec.Model)
	if model == "" {
		model = strings.TrimSpace(rec.Alias)
	}
	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pace.runningModel == model || state.pace.runningModel == "" {
		state.pace.runningModel = ""
		state.pace.runningSince = time.Time{}
	}
	if rec.Failed || model == "" || rec.Latency <= 0 || rec.Detail.OutputTokens <= 0 {
		return okEnvelopeJSON(`{}`)
	}
	state.pace.model = model
	state.pace.outputTokens = rec.Detail.OutputTokens
	state.pace.latency = rec.Latency
	state.pace.tokensPerSec = float64(rec.Detail.OutputTokens) / rec.Latency.Seconds()
	state.pace.at = now
	return okEnvelopeJSON(`{}`)
}

func currentPace() hubPace {
	state.mu.Lock()
	defer state.mu.Unlock()
	pace := state.pace
	if !pace.runningSince.IsZero() && time.Since(pace.runningSince) > 2*time.Minute {
		pace.runningModel = ""
	}
	out := hubPace{RunningModel: pace.runningModel}
	if pace.model == "" || pace.at.IsZero() {
		return out
	}
	out.Model = pace.model
	out.TokensPerSec = pace.tokensPerSec
	out.OutputTokens = pace.outputTokens
	out.LatencyMS = pace.latency.Milliseconds()
	out.At = pace.at.UTC().Format(time.RFC3339)
	return out
}
