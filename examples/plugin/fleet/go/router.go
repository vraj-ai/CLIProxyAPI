// cpa router: exposes the virtual model "cpa router" through the plugin
// executor path. CPA boundary intercepts this model only; concrete models
// keep the built-in provider path untouched. Herdr owns orchestration —
// this file holds the domain types and the executor boundary; herdr.go
// holds the live adapter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	virtualRouterModel     = "cpa router"
	routerProvider         = "fleet-router"
	resultMarkerBegin      = "CPA-RESULT-BEGIN-"
	resultMarkerEnd        = "CPA-RESULT-END-"
	defaultRouterTimeoutMS = 120000
	defaultRouterReadLines = 200
)

// routerError carries an actionable code plus the HTTP status a client should
// see; the ABI envelope surfaces both.
type routerError struct {
	code      string
	message   string
	status    int
	retryable bool
}

func (e *routerError) Error() string { return e.message }

func routerErr(code, message string, status int) *routerError {
	return &routerError{code: code, message: message, status: status}
}

// leadRequest is one unit of orchestration work. The correlation marker is
// echoed back by the lead so a terminal read-back can prove the text belongs
// to this request — herdr's wait does not track turns, so the marker is the
// only reliable completion boundary.
type leadRequest struct {
	CorrelationID string
	Agent         string // resolved herdr pane id
	Role          string // lead, sidekick, or lead_finalize
	Task          string
	Model         string
	Effort        string
	Deadline      time.Duration
}

type leadResult struct {
	Text         string
	Agent        string
	Verification string // "ok", "failed", or "" when unreported
}

// leadBackend is the seam between the executor and the live orchestrator.
// Production uses cliHerdr; the routing-contract suite substitutes a fake.
type leadBackend interface {
	// agents reports the live orchestrator agents selection can target.
	agents(ctx context.Context) ([]herdrAgent, error)
	runLead(ctx context.Context, req leadRequest) (leadResult, error)
}

// routeDecision is the redacted record diagnostics expose later.
type routeDecision struct {
	CorrelationID string       `json:"correlation_id"`
	At            string       `json:"at"`
	Model         string       `json:"model"`
	Outcome       string       `json:"outcome"`
	Reason        string       `json:"reason,omitempty"`
	Agent         string       `json:"agent,omitempty"`
	Effort        string       `json:"effort,omitempty"`
	Escalation    string       `json:"escalation,omitempty"`
	Sidekick      string       `json:"sidekick,omitempty"`
	Skipped       []skipReason `json:"skipped,omitempty"`
}

var corrCounter atomic.Int64

func correlationID() string {
	return fmt.Sprintf("cpa-%d-%04d", time.Now().UnixMilli(), corrCounter.Add(1))
}

func routerConfig() pluginConfig {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.config
}

func currentQuota() quotaSource {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.quota != nil {
		return state.quota
	}
	return defaultQuota()
}

func currentBackend() leadBackend {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.backend != nil {
		return state.backend
	}
	return defaultHerdr()
}

func recordDecision(d routeDecision) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.decisions = append(state.decisions, d)
	if len(state.decisions) > 64 {
		state.decisions = state.decisions[len(state.decisions)-64:]
	}
}

// ---------------------------------------------------------------------------
// registration + dispatch

func modelRegistration() pluginapi.ModelRegistrationResponse {
	resp := pluginapi.ModelRegistrationResponse{Provider: routerProvider}
	if !routerConfig().RouterEnabled {
		return resp
	}
	resp.Models = []pluginapi.ModelInfo{{
		ID:          virtualRouterModel,
		Object:      "model",
		OwnedBy:     routerProvider,
		DisplayName: "CPA Router (Herdr-orchestrated)",
		Description: "Virtual model: routes through Herdr lead/sidekick orchestration with subscription pacing.",
	}}
	return resp
}

// executorCallRequest mirrors the host's rpcExecutorRequest: the embedded
// pluginapi.ExecutorRequest marshals with capitalized Go field names, plus
// the snake_case stream/callback ids the rpc wrapper adds.
type executorCallRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorEnvelope("bad_request", "executor request did not parse: "+err.Error()), nil
	}
	payload, err := routeToLead(&req)
	if err != nil {
		return routerErrorEnvelope(err), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: payload,
		Headers: http.Header{"content-type": {"application/json"}},
	})
}

func routeToLead(req *executorCallRequest) ([]byte, error) {
	cfg := routerConfig()
	if !cfg.RouterEnabled {
		return nil, routerErr("router_disabled", "cpa router is not enabled in the fleet plugin config", http.StatusNotFound)
	}
	if req.Model != virtualRouterModel {
		return nil, routerErr("unsupported_model", fmt.Sprintf("cpa router executor only serves %q; got %q", virtualRouterModel, req.Model), http.StatusBadRequest)
	}
	task, err := taskText(req.Payload)
	if err != nil {
		return nil, err
	}
	corr := correlationID()
	timeout := cfg.RouterTimeoutMS
	if timeout <= 0 {
		timeout = defaultRouterTimeoutMS
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	backend := currentBackend()
	live, err := backend.agents(ctx)
	if err != nil {
		return nil, err
	}
	quota, err := currentQuota().fetch(ctx)
	if err != nil {
		recordDecision(routeDecision{CorrelationID: corr, At: time.Now().UTC().Format(time.RFC3339), Model: req.Model, Outcome: "failed", Reason: errCode(err)})
		return nil, err
	}
	sel, err := selectCandidate(live, requestedEffort(req.Payload), quota, nowFunc(), approvalGranted(req), "")
	if err != nil {
		recordDecision(routeDecision{CorrelationID: corr, At: time.Now().UTC().Format(time.RFC3339), Model: req.Model, Outcome: "failed", Reason: errCode(err)})
		return nil, err
	}
	res, err := backend.runLead(ctx, leadRequest{
		CorrelationID: corr,
		Agent:         sel.Agent.PaneID,
		Role:          roleLead,
		Task:          task,
		Model:         sel.Chosen.Model,
		Effort:        sel.Effort,
		Deadline:      time.Duration(timeout) * time.Millisecond,
	})
	if err != nil {
		recordDecision(routeDecision{CorrelationID: corr, At: time.Now().UTC().Format(time.RFC3339), Model: req.Model, Outcome: "failed", Reason: errCode(err), Agent: sel.Agent.PaneID, Effort: sel.Effort, Skipped: sel.Skipped})
		return nil, err
	}
	triggers := escalationTriggers(req, res)
	if len(triggers) == 0 {
		recordDecision(routeDecision{CorrelationID: corr, At: time.Now().UTC().Format(time.RFC3339), Model: sel.Chosen.Model, Outcome: "lead_completed", Agent: res.Agent, Effort: sel.Effort, Skipped: sel.Skipped})
		return openaiCompletion(corr, res.Text), nil
	}
	return escalate(ctx, backend, req, sel, quota, live, res, task, corr, triggers, time.Duration(timeout)*time.Millisecond)
}

// escalate adds a read-only sidekick between the lead's draft and its final
// answer. The sidekick is selected by the same eligibility walk minus the
// lead's candidate — scrutiny must come from an independent provider — and
// fails closed when none is eligible. The lead always emits the final text.
func escalate(ctx context.Context, backend leadBackend, req *executorCallRequest, sel selection, quota map[string]providerQuota, live []herdrAgent, draft leadResult, task, corr string, triggers []string, deadline time.Duration) ([]byte, error) {
	base := routeDecision{
		CorrelationID: corr, At: time.Now().UTC().Format(time.RFC3339), Model: sel.Chosen.Model,
		Agent: sel.Agent.PaneID, Effort: sel.Effort, Skipped: sel.Skipped,
		Escalation: strings.Join(triggers, ","),
	}
	side, err := selectCandidate(live, "", quota, nowFunc(), approvalGranted(req), sel.Chosen.Label)
	if err != nil {
		base.Outcome = "failed"
		base.Reason = "no_eligible_sidekick"
		recordDecision(base)
		return nil, routerErr("no_eligible_sidekick",
			"escalation ("+base.Escalation+") required but no independent candidate is eligible — "+err.Error(),
			http.StatusServiceUnavailable)
	}
	review, err := backend.runLead(ctx, leadRequest{
		CorrelationID: corr + "-side",
		Agent:         side.Agent.PaneID,
		Role:          roleSidekick,
		Task:          sidekickBrief(task, draft.Text),
		Model:         side.Chosen.Model,
		Effort:        side.Effort,
		Deadline:      deadline,
	})
	if err != nil {
		base.Outcome = "failed"
		base.Reason = "sidekick_" + errCode(err)
		base.Sidekick = side.Chosen.Label + "@" + side.Agent.PaneID
		recordDecision(base)
		return nil, err
	}
	final, err := backend.runLead(ctx, leadRequest{
		CorrelationID: corr + "-final",
		Agent:         sel.Agent.PaneID,
		Role:          roleFinalize,
		Task:          finalizeBrief(task, draft.Text, review.Text),
		Model:         sel.Chosen.Model,
		Effort:        sel.Effort,
		Deadline:      deadline,
	})
	if err != nil {
		base.Outcome = "failed"
		base.Reason = errCode(err)
		base.Sidekick = side.Chosen.Label + "@" + side.Agent.PaneID
		recordDecision(base)
		return nil, err
	}
	base.Outcome = "escalated_completed"
	base.Sidekick = side.Chosen.Label + "@" + side.Agent.PaneID
	base.Skipped = append(base.Skipped, side.Skipped...)
	recordDecision(base)
	return openaiCompletion(corr, final.Text), nil
}

func errCode(err error) string {
	if re, ok := err.(*routerError); ok {
		return re.code
	}
	return "plugin_error"
}

func routerErrorEnvelope(err error) []byte {
	if re, ok := err.(*routerError); ok {
		return errorEnvelopeStatus(re.code, re.message, re.status, re.retryable)
	}
	return errorEnvelope("plugin_error", err.Error())
}

// ---------------------------------------------------------------------------
// request/response shaping

// taskText pulls the user's request out of the openai chat payload. The
// executor declares openai as its only input format, so the host translates
// other client protocols before this point.
func taskText(payload []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil || doc == nil {
		return "", routerErr("bad_request", "executor payload is not a JSON object", http.StatusBadRequest)
	}
	msgs, ok := doc["messages"].([]any)
	if !ok {
		return "", routerErr("bad_request", "executor payload has no messages array", http.StatusBadRequest)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				return c, nil
			}
		case []any:
			var parts []string
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
			if joined := strings.Join(parts, "\n"); strings.TrimSpace(joined) != "" {
				return joined, nil
			}
		}
	}
	return "", routerErr("bad_request", "executor payload has no user message text", http.StatusBadRequest)
}

// extractMarked returns the text the lead framed between its result markers,
// scanning from the end so an echoed prompt line cannot be mistaken for the
// real answer.
func extractMarked(text, corr string) (string, bool) {
	end := resultMarkerEnd + corr
	i := strings.LastIndex(text, end)
	if i < 0 {
		return "", false
	}
	begin := resultMarkerBegin + corr
	j := strings.LastIndex(text[:i], begin)
	if j < 0 {
		return "", false
	}
	out := strings.TrimSpace(text[j+len(begin) : i])
	return out, out != ""
}

func openaiCompletion(corr, text string) []byte {
	resp := map[string]any{
		"id":      "chatcmpl-" + corr,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   virtualRouterModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	}
	raw, _ := json.Marshal(resp)
	return raw
}
