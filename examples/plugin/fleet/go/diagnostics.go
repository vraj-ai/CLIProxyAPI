// diagnostics.go: redacted operational evidence for the router. The /router
// management resource reports correlation, routing reason, selected
// model/effort, quota freshness, escalation, and outcome per decision, plus a
// read-only readiness summary. Nothing here carries prompt text, provider
// credentials, or secret-shaped strings — free-text fields are scrubbed at
// record time and the serialized body is scrubbed again on the way out.
package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	pluginapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`xox[bapors]-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{10,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{10,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)[=:]\s*["']?[^\s"']{8,}`),
}

// redact scrubs secret-shaped strings. Operator-configured sentinels
// (config redact_sentinels) are literal strings that must never appear in
// diagnostics regardless of shape.
func redact(s string) string {
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[redacted]")
	}
	state.mu.Lock()
	sentinels := append([]string(nil), state.sentinels...)
	state.mu.Unlock()
	for _, sentinel := range sentinels {
		if sentinel != "" {
			s = strings.ReplaceAll(s, sentinel, "[redacted]")
		}
	}
	return s
}

// quotaFreshness summarizes snapshot freshness for decision evidence.
func quotaFreshness(quota map[string]providerQuota, now time.Time) string {
	if quota == nil {
		return "unknown"
	}
	for _, p := range quota {
		if p.Stale || (!p.ExpiresAt.IsZero() && now.After(p.ExpiresAt)) {
			return "stale"
		}
	}
	return "fresh"
}

// readinessReport is the read-only readiness evidence: what is registered,
// what the executor is compatible with, and which provider-policy exclusions
// are enforced. It deliberately names its own scope so offline evidence can
// never be read as live acceptance.
func readinessReport() map[string]any {
	cfg := routerConfig()
	registered := false
	for _, m := range modelRegistration().Models {
		if m.ID == virtualRouterModel {
			registered = true
		}
	}
	var excluded []string
	for _, c := range preferenceOrder {
		if !c.PolicyOK {
			excluded = append(excluded, c.Label)
		}
	}
	return map[string]any{
		"scope":               "readiness evidence only — not live inference or deployment acceptance",
		"router_enabled":      cfg.RouterEnabled,
		"model_registered":    registered,
		"model":               virtualRouterModel,
		"executor_identifier": routerProvider,
		"input_formats":       []string{"openai"},
		"output_formats":      []string{"openai"},
		"orchestrator":        "herdr",
		"quota_source":        "openusage --force (openusage.limits.v1)",
		"policy_excluded":     excluded,
		"client_cancel":       "plugin ABI has no mid-call cancel; a disconnect still consumes router_timeout_ms",
	}
}

func routerDiagnostics() ([]byte, error) {
	state.mu.Lock()
	decisions := append([]routeDecision(nil), state.decisions...)
	state.mu.Unlock()
	body, err := json.Marshal(map[string]any{
		"generated_at": nowFunc().UTC().Format(time.RFC3339),
		"plugin":       "fleet",
		"readiness":    readinessReport(),
		"decisions":    decisions,
	})
	if err != nil {
		return nil, err
	}
	resp := pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"content-type": {"application/json"}},
		Body:       []byte(redact(string(body))),
	}
	return okEnvelope(resp)
}
