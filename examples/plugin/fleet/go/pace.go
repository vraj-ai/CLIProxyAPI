package main

import (
	"encoding/json"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// paceView is the hub's rate surface: the latest completed model's tokens per
// second computed from output tokens and latency, plus models whose request
// has started and not finished. A running model never carries a rate.
type paceView struct {
	Model        string   `json:"model,omitempty"`
	TPS          float64  `json:"tps,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	LatencyMS    int64    `json:"latency_ms,omitempty"`
	At           string   `json:"at,omitempty"`
	Running      []string `json:"running,omitempty"`
}

type paceTracker struct {
	running map[string]string // request id -> model
	done    *paceView         // latest completed
}

func (p *paceTracker) markRunning(requestID, model string) {
	if p.running == nil {
		p.running = map[string]string{}
	}
	if requestID == "" || model == "" {
		return
	}
	p.running[requestID] = model
}

func (p *paceTracker) complete(requestID string) {
	delete(p.running, requestID)
}

func (p *paceTracker) observe(record pluginapiUsageRecord) {
	if record.Failed || record.Model == "" {
		return
	}
	latency := record.Latency
	output := record.Detail.OutputTokens
	view := paceView{
		Model:        record.Model,
		OutputTokens: output,
		At:           record.RequestedAt.UTC().Format(time.RFC3339),
	}
	if latency > 0 {
		view.LatencyMS = latency.Milliseconds()
		view.TPS = float64(output) / latency.Seconds()
	}
	p.done = &view
}

// pluginapiUsageRecord is the subset of pluginapi.UsageRecord the tracker
// needs. It keeps the tracker testable without the full provider record and,
// by construction, keeps API key material out of the pace view.
type pluginapiUsageRecord struct {
	Model       string
	RequestedAt time.Time
	Latency     time.Duration
	Failed      bool
	Detail      struct {
		OutputTokens int64
	}
}

// buildPaceView snapshots the tracker for the hub state. Callers hold
// state.mu.
func buildPaceView() paceView {
	out := paceView{}
	if state.pace.done != nil {
		out = *state.pace.done
	}
	running := make([]string, 0, len(state.pace.running))
	seen := map[string]bool{}
	for _, model := range state.pace.running {
		if model != "" && !seen[model] {
			seen[model] = true
			running = append(running, model)
		}
	}
	out.Running = running
	if out.Model == "" && len(running) > 0 {
		out.TPS = 0
		out.OutputTokens = 0
		out.LatencyMS = 0
		out.At = ""
	}
	return out
}

func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return errorEnvelope("invalid_usage_record", "usage record did not parse"), nil
	}
	converted := pluginapiUsageRecord{
		Model:       record.Model,
		RequestedAt: record.RequestedAt,
		Latency:     record.Latency,
		Failed:      record.Failed,
	}
	converted.Detail.OutputTokens = record.Detail.OutputTokens
	state.mu.Lock()
	state.pace.observe(converted)
	running := buildPaceView()
	state.mu.Unlock()
	return okEnvelope(running)
}

func handleRequestComplete(raw []byte) ([]byte, error) {
	var completion pluginapi.RequestCompletion
	if err := json.Unmarshal(raw, &completion); err != nil {
		return errorEnvelope("invalid_completion", "completion did not parse"), nil
	}
	state.mu.Lock()
	state.pace.complete(completion.RequestID)
	state.mu.Unlock()
	return okEnvelope(map[string]string{"outcome": string(completion.Outcome)})
}
