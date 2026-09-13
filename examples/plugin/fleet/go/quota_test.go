package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	pluginabi "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type fakeQuota struct {
	snap map[string]providerQuota
	err  error
}

func (f fakeQuota) fetch(_ context.Context) (map[string]providerQuota, error) {
	return f.snap, f.err
}

var testNow = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

func freezeNow(t *testing.T) {
	t.Helper()
	old := nowFunc
	nowFunc = func() time.Time { return testNow }
	t.Cleanup(func() { nowFunc = old })
}

// freshProvider builds a healthy consumption window anchored at testNow.
func freshProvider(remaining float64) providerQuota {
	return freshProviderAt(remaining, nowFunc())
}

func freshProviderAt(remaining float64, anchor time.Time) providerQuota {
	return providerQuota{
		FetchedAt: anchor.Add(-time.Minute),
		ExpiresAt: anchor.Add(5 * time.Minute),
		Resources: map[string]quotaResource{
			"weekly": {Kind: "consumption", Remaining: remaining, Limit: 100,
				ResetsAt: anchor.Add(7 * 24 * time.Hour), WindowSeconds: 604800, Unit: "percent"},
		},
	}
}

func healthyQuota() map[string]providerQuota {
	return map[string]providerQuota{
		"codex": freshProvider(80), "devin": freshProvider(80),
		"grok": freshProvider(80), "claude": freshProvider(80),
		"opencode": freshProvider(80),
	}
}

// Real `openusage --force` output captured 2026-09-12, trimmed to the fields
// the normalizer consumes. Proves the wire schema, not a guess at it.
const realOpenusageFixture = `{"errors":[],"generatedAt":"2026-09-12T16:45:27.328Z","providers":{"codex":{"displayName":"Codex","expiresAt":"2026-09-12T16:50:23.372Z","fetchedAt":"2026-09-12T16:45:23.372Z","plan":"Plus","resources":{"credits":{"available":0,"kind":"balance","unit":"credits"},"session":{"kind":"consumption","limit":100,"remaining":36,"resetsAt":"2026-09-12T19:37:27.000Z","unit":"percent","used":64,"utilization":0.64,"windowSeconds":18000},"weekly":{"kind":"consumption","limit":100,"remaining":79,"resetsAt":"2026-09-19T09:37:27.000Z","unit":"percent","used":21,"utilization":0.21,"windowSeconds":604800}},"stale":false},"grok":{"displayName":"Grok","expiresAt":"2026-09-12T16:50:22.857Z","fetchedAt":"2026-09-12T16:45:22.857Z","plan":"X Premium+","resources":{"weekly":{"kind":"consumption","limit":100,"remaining":94,"resetsAt":"2026-09-17T13:32:42.093Z","unit":"percent","used":6,"utilization":0.06,"windowSeconds":604800}},"stale":false},"openrouter":{"displayName":"OpenRouter","expiresAt":"2026-09-12T16:50:22.379Z","fetchedAt":"2026-09-12T16:45:22.379Z","plan":"Pay as you go","resources":{"balance":{"available":5.47,"kind":"balance","unit":"usd"}},"stale":false}},"schema":"openusage.limits.v1"}`

func TestParseRealOpenusage(t *testing.T) {
	snap, err := parseOpenusage([]byte(realOpenusageFixture))
	if err != nil {
		t.Fatal(err)
	}
	codex := snap["codex"]
	if codex.Resources["weekly"].Remaining != 79 || codex.Resources["weekly"].WindowSeconds != 604800 {
		t.Fatalf("codex weekly = %+v", codex.Resources["weekly"])
	}
	if codex.Resources["session"].WindowSeconds != 18000 {
		t.Fatalf("codex session = %+v", codex.Resources["session"])
	}
	if codex.Resources["credits"].Kind != "balance" {
		t.Fatalf("codex credits = %+v", codex.Resources["credits"])
	}
	if snap["openrouter"].Resources["balance"].Kind != "balance" {
		t.Fatal("openrouter balance not normalized")
	}
}

func TestQuotaGatePacing(t *testing.T) {
	// Behind pace: 5% remaining with half the window left → paced out.
	behind := freshProviderAt(5, testNow)
	behind.Resources["weekly"] = quotaResource{Kind: "consumption", Remaining: 5, Limit: 100,
		ResetsAt: testNow.Add(42 * time.Hour), WindowSeconds: 604800, Unit: "percent"}
	snap := map[string]providerQuota{"codex": behind}
	if got := quotaGate("codex", snap, testNow, false); !strings.HasPrefix(got, "quota_paced:weekly") {
		t.Fatalf("gate = %q", got)
	}
	if got := quotaGate("codex", healthyQuota(), testNow, false); got != "" {
		t.Fatalf("healthy gate = %q", got)
	}
}

func TestQuotaGateStates(t *testing.T) {
	stale := freshProviderAt(80, testNow)
	stale.Stale = true
	if got := quotaGate("codex", map[string]providerQuota{"codex": stale}, testNow, false); got != "quota_stale" {
		t.Fatalf("stale gate = %q", got)
	}
	expired := freshProviderAt(80, testNow)
	expired.ExpiresAt = testNow.Add(-time.Minute)
	if got := quotaGate("codex", map[string]providerQuota{"codex": expired}, testNow, false); got != "quota_stale" {
		t.Fatalf("expired gate = %q", got)
	}
	if got := quotaGate("codex", map[string]providerQuota{}, testNow, false); got != "quota_unknown" {
		t.Fatalf("missing provider gate = %q", got)
	}
	exhausted := freshProviderAt(0, testNow)
	if got := quotaGate("codex", map[string]providerQuota{"codex": exhausted}, testNow, false); got != "quota_exhausted:weekly" {
		t.Fatalf("exhausted gate = %q", got)
	}
	paid := providerQuota{FetchedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
		Resources: map[string]quotaResource{"balance": {Kind: "balance", Remaining: 5, Unit: "usd"}}}
	if got := quotaGate("openrouter", map[string]providerQuota{"openrouter": paid}, testNow, false); got != "approval_required" {
		t.Fatalf("balance gate = %q", got)
	}
	if got := quotaGate("openrouter", map[string]providerQuota{"openrouter": paid}, testNow, true); got != "" {
		t.Fatalf("approved balance gate = %q", got)
	}
}

func TestPacingChangesRoute(t *testing.T) {
	freezeNow(t)
	quota := healthyQuota()
	paced := freshProviderAt(80, testNow)
	paced.Resources["weekly"] = quotaResource{Kind: "consumption", Remaining: 2, Limit: 100,
		ResetsAt: testNow.Add(20 * time.Hour), WindowSeconds: 604800, Unit: "percent"}
	quota["codex"] = paced

	backend := &fakeBackend{
		result: leadResult{Text: "ok", Agent: "w2:p3"},
		live: []herdrAgent{
			{Name: "pi", Status: "idle", PaneID: "w1B:p1"},
			{Name: "grok", Status: "idle", PaneID: "w2:p3"},
		},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	state.mu.Lock()
	state.quota = fakeQuota{snap: quota}
	state.mu.Unlock()

	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if !env.OK {
		t.Fatalf("env = %+v", env)
	}
	call := backend.calls[0]
	if call.Model != "grok-4.6" || call.Agent != "w2:p3" {
		t.Fatalf("paced route = %+v", call)
	}
	state.mu.Lock()
	d := state.decisions[len(state.decisions)-1]
	state.mu.Unlock()
	foundPaced := false
	for _, s := range d.Skipped {
		if strings.HasPrefix(s.Reason, "quota_paced:weekly") {
			foundPaced = true
		}
	}
	if !foundPaced {
		t.Fatalf("decision skipped = %+v", d.Skipped)
	}
}

func TestQuotaUnknownFailsClosed(t *testing.T) {
	freezeNow(t)
	backend := &fakeBackend{
		result: leadResult{Text: "ok"},
		live:   livePi,
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	state.mu.Lock()
	state.quota = fakeQuota{snap: map[string]providerQuota{}}
	state.mu.Unlock()
	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error == nil || env.Error.Code != "no_eligible_candidate" {
		t.Fatalf("env = %+v", env)
	}
	if !strings.Contains(env.Error.Message, "quota_unknown") {
		t.Fatalf("message = %q", env.Error.Message)
	}
	if len(backend.calls) != 0 {
		t.Fatal("unknown quota reached the lead backend")
	}
}

func TestApprovalScopedToRetry(t *testing.T) {
	freezeNow(t)
	// A test-only paid-fallback candidate proves the gate: balance resources
	// are never consumed without the explicit retry header.
	paidCandidate := candidate{Label: "paid fallback", Kind: "opencode", Model: "m", Subscription: "openrouter", PolicyOK: true, Evidence: "test"}
	old := preferenceOrder
	preferenceOrder = append([]candidate{}, paidCandidate)
	t.Cleanup(func() { preferenceOrder = old })

	paid := providerQuota{FetchedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
		Resources: map[string]quotaResource{"balance": {Kind: "balance", Remaining: 5, Unit: "usd"}}}
	backend := &fakeBackend{
		result: leadResult{Text: "ok", Agent: "w9:p1"},
		live:   []herdrAgent{{Name: "opencode", Status: "idle", PaneID: "w9:p1"}},
	}
	resetRouter(pluginConfig{RouterEnabled: true}, backend)
	state.mu.Lock()
	state.quota = fakeQuota{snap: map[string]providerQuota{"openrouter": paid}}
	state.mu.Unlock()

	env := callMethod(t, pluginabi.MethodExecutorExecute,
		execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`))
	if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "approval_required") {
		t.Fatalf("no-approval env = %+v", env)
	}
	if len(backend.calls) != 0 {
		t.Fatal("paid route ran without approval")
	}

	req := execRequest(virtualRouterModel, `{"messages":[{"role":"user","content":"hi"}]}`)
	req.Headers = http.Header{"X-Fleet-Approve": {"retry"}}
	env = callMethod(t, pluginabi.MethodExecutorExecute, req)
	if !env.OK {
		t.Fatalf("approved env = %+v", env)
	}
	if len(backend.calls) != 1 {
		t.Fatal("approved retry did not reach the lead")
	}
}
