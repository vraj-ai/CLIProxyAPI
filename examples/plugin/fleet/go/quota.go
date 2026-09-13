// quota.go: openusage adapter and subscription pacing. Quota truth comes from
// `openusage --force` (schema openusage.limits.v1); anything unknown, stale,
// exhausted, or balance-gated never authorizes a route — the candidate is
// skipped with an explicit reason instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// nowFunc is the controllable clock (repo convention for TTL/pacing tests).
var nowFunc = time.Now

// quotaResource is one normalized allowance window inside a provider.
type quotaResource struct {
	Kind          string // "consumption" percent windows or "balance" money
	Remaining     float64
	Limit         float64
	ResetsAt      time.Time
	WindowSeconds int64
	Unit          string
}

// providerQuota is one normalized openusage provider entry.
type providerQuota struct {
	Plan      string
	Stale     bool
	FetchedAt time.Time
	ExpiresAt time.Time
	Resources map[string]quotaResource
}

// quotaSource fetches the normalized provider map. Production runs
// `openusage --force`; the contract suite injects fixtures.
type quotaSource interface {
	fetch(ctx context.Context) (map[string]providerQuota, error)
}

type cliQuota struct {
	bin string
	run commandRunner
}

func defaultQuota() *cliQuota {
	return &cliQuota{bin: resolveBin("openusage"), run: execRunner}
}

type openusageDoc struct {
	Schema      string `json:"schema"`
	GeneratedAt string `json:"generatedAt"`
	Providers   map[string]struct {
		Plan      string `json:"plan"`
		FetchedAt string `json:"fetchedAt"`
		ExpiresAt string `json:"expiresAt"`
		Stale     bool   `json:"stale"`
		Resources map[string]struct {
			Kind          string   `json:"kind"`
			Remaining     *float64 `json:"remaining"`
			Limit         *float64 `json:"limit"`
			Used          *float64 `json:"used"`
			Utilization   *float64 `json:"utilization"`
			ResetsAt      string   `json:"resetsAt"`
			Unit          string   `json:"unit"`
			WindowSeconds int64    `json:"windowSeconds"`
			Available     *float64 `json:"available"`
		} `json:"resources"`
	} `json:"providers"`
}

func parseOpenusage(out []byte) (map[string]providerQuota, error) {
	var doc openusageDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("openusage output did not parse: %w", err)
	}
	if doc.Schema != "openusage.limits.v1" {
		return nil, fmt.Errorf("openusage schema %q unsupported", doc.Schema)
	}
	snap := make(map[string]providerQuota, len(doc.Providers))
	for name, p := range doc.Providers {
		pq := providerQuota{Plan: p.Plan, Stale: p.Stale, Resources: map[string]quotaResource{}}
		if t, err := time.Parse(time.RFC3339, p.FetchedAt); err == nil {
			pq.FetchedAt = t
		}
		if p.ExpiresAt == "" {
			pq.Stale = true
		} else if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
			pq.Stale = true
		} else {
			pq.ExpiresAt = t
		}
		for rname, r := range p.Resources {
			qr := quotaResource{Kind: r.Kind, Unit: r.Unit, WindowSeconds: r.WindowSeconds}
			if r.Remaining != nil {
				qr.Remaining = *r.Remaining
			}
			if r.Limit != nil {
				qr.Limit = *r.Limit
			}
			if qr.Limit == 0 && r.Available != nil {
				qr.Remaining = *r.Available
			}
			if r.ResetsAt != "" {
				t, err := time.Parse(time.RFC3339, r.ResetsAt)
				if err != nil {
					pq.Stale = true
				} else {
					qr.ResetsAt = t
				}
			}
			pq.Resources[rname] = qr
		}
		snap[name] = pq
	}
	return snap, nil
}

func (q *cliQuota) fetch(ctx context.Context) (map[string]providerQuota, error) {
	out, err := q.run(ctx, q.bin, "--force")
	if err != nil {
		return nil, routerErr("quota_unavailable", fmt.Sprintf("openusage failed: %v", err), http.StatusServiceUnavailable)
	}
	snap, err := parseOpenusage(out)
	if err != nil {
		return nil, routerErr("quota_unavailable", err.Error(), http.StatusServiceUnavailable)
	}
	return snap, nil
}

// quotaGate reports "" when the provider's allowance can cover the request,
// else the explicit skip reason. Balance-only providers are approval-gated:
// spending money needs an explicit retry approval, never an automatic route.
func quotaGate(sub string, snap map[string]providerQuota, now time.Time, approved bool) string {
	p, ok := snap[sub]
	if !ok {
		return "quota_unknown"
	}
	if p.Stale || (!p.ExpiresAt.IsZero() && now.After(p.ExpiresAt)) {
		return "quota_stale"
	}
	hasConsumption := false
	for _, r := range p.Resources {
		if r.Kind == "consumption" {
			hasConsumption = true
		}
	}
	if len(p.Resources) == 0 {
		return "quota_unknown"
	}
	if !hasConsumption {
		if approved {
			return ""
		}
		return "approval_required"
	}
	names := make([]string, 0, len(p.Resources))
	for name := range p.Resources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := p.Resources[name]
		if r.Kind != "consumption" {
			continue
		}
		if r.Remaining <= 0 {
			return "quota_exhausted:" + name
		}
		if r.Limit > 0 && r.WindowSeconds > 0 && !r.ResetsAt.IsZero() {
			// Sustainable pace: burn fraction must not outrun elapsed window
			// fraction. Below the elapsed floor any usage looks unsustainable,
			// so a fresh window never misfires.
			left := r.ResetsAt.Sub(now).Seconds()
			if left < 0 {
				left = 0
			}
			leftFrac := left / float64(r.WindowSeconds)
			if leftFrac > 1 {
				leftFrac = 1
			}
			elapsed := 1 - leftFrac
			if elapsed >= 0.05 && (1-r.Remaining/r.Limit)/elapsed > 1 {
				return "quota_paced:" + name + " resets " + r.ResetsAt.UTC().Format(time.RFC3339)
			}
		}
	}
	return ""
}

// approvalGranted is the explicit, retry-scoped approval signal: the client
// sends `x-fleet-approve: retry`. Nothing else authorizes a gated route.
func approvalGranted(req *executorCallRequest) bool {
	for _, v := range req.Headers.Values("x-fleet-approve") {
		if v == "retry" {
			return true
		}
	}
	return false
}
