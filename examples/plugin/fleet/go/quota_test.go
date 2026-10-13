package main

import (
	"context"
	"testing"
	"time"
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

func TestParseOpenusageCorruptExpiryIsStale(t *testing.T) {
	snap, err := parseOpenusage([]byte(`{"schema":"openusage.limits.v1","providers":{"codex":{"plan":"Plus","fetchedAt":"nope","expiresAt":"also-nope","stale":false,"resources":{"weekly":{"kind":"consumption","limit":100,"remaining":80,"resetsAt":"bad","windowSeconds":604800}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !snap["codex"].Stale {
		t.Fatal("corrupt timestamps must fail closed as stale")
	}
	if !snap["codex"].ExpiresAt.IsZero() {
		t.Fatal("unparseable expiry must not become a real time")
	}
}

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
