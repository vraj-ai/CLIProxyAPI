// quota.go: openusage adapter and subscription pacing. Quota truth comes from
// `openusage --force` (schema openusage.limits.v1); anything unknown, stale,
// exhausted, or balance-gated never authorizes a route — the candidate is
// skipped with an explicit reason instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		return nil, fmt.Errorf("openusage failed: %w", err)
	}
	snap, err := parseOpenusage(out)
	if err != nil {
		return nil, err
	}
	return snap, nil
}
