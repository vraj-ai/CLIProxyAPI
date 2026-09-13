// registry.go: the capability registry. Every candidate field is backed by
// recorded evidence (docs/cpa-router.md); anything unverified makes the
// candidate skip with an explicit reason rather than route on a guess.
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// candidate is one entry in the ordered preference list.
type candidate struct {
	Label        string   // preference label from the spec, e.g. "astra low"
	Kind         string   // herdr agent kind that serves this candidate
	Model        string   // verified model identifier, empty when unverified
	Effort       string   // default effort carried by the preference label
	Efforts      []string // verified supported effort set
	EffortOpaque bool     // effort is not wire-forwarded; the agent's own config owns it
	Subscription string   // openusage provider key backing this route
	PolicyOK     bool     // provider policy permits this route
	Evidence     string   // where the verification lives
}

// preferenceOrder is issue #8's list, top first. Claude stays policy-excluded:
// Anthropic bans third-party OAuth use (docs/TOS-SAFETY.md, SUBSCRIPTIONS.md)
// and no permitted CPA access has been established.
var preferenceOrder = []candidate{
	{
		Label: "astra low", Kind: "pi", Model: "gpt-6-astra", Effort: "low",
		Efforts:      []string{"low", "medium", "high", "xhigh", "max"},
		Subscription: "codex", PolicyOK: true,
		Evidence: "live pi pane shows `gpt-6-astra Cliproxy low`; docs/CLIPROXYAPI.md: pi preserves low/medium/high/xhigh/max",
	},
	{
		Label: "swe 2 max", Kind: "devin", Model: "swe-2-max", Effort: "max",
		EffortOpaque: true,
		Subscription: "devin", PolicyOK: true,
		Evidence: "herdr supports kind `devin`; openusage provider `devin` (Pro); model name per this session's own harness",
	},
	{
		Label: "opus 5 medium", Kind: "claude", Model: "claude", Effort: "medium",
		Subscription: "claude", PolicyOK: false,
		Evidence: "herdr supports kind `claude`; SUBSCRIPTIONS.md keeps Claude out of the pool (ToS)",
	},
	{
		Label: "grok 4.6 high", Kind: "grok", Model: "grok-4.6", Effort: "high",
		EffortOpaque: true,
		Subscription: "grok", PolicyOK: true,
		Evidence: "herdr supports kind `grok`; wire id evidenced by or/grok-4.6-or + ocg/grok-4.6-go aliases; openusage provider `grok` (X Premium+)",
	},
	{
		Label: "muse spark 1.3 contributer max", Kind: "muse", Model: "", Effort: "contributer max",
		EffortOpaque: true,
		Subscription: "", PolicyOK: true,
		Evidence: "herdr supports kind `muse`; no openusage provider or subscription record exists for muse",
	},
}

type skipReason struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// selection is the routing decision: the chosen candidate, the live agent it
// resolved to, and why every earlier candidate was skipped.
type selection struct {
	Chosen  candidate
	Agent   herdrAgent
	Effort  string
	Skipped []skipReason
}

// selectCandidate walks the preference order and returns the first candidate
// whose every forwarded fact is verified and whose live agent exists.
// reqEffort is the client-requested effort ("" means none required).
func selectCandidate(live []herdrAgent, reqEffort string, quota map[string]providerQuota, now time.Time, approved bool, exclude string) (selection, error) {
	var skipped []skipReason
	for _, c := range preferenceOrder {
		if c.Label == exclude {
			skipped = append(skipped, skipReason{c.Label, "lead_holds_ownership"})
			continue
		}
		agent, effort, reason := candidateGate(c, live, reqEffort, quota, now, approved)
		if reason != "" {
			skipped = append(skipped, skipReason{c.Label, reason})
			continue
		}
		return selection{Chosen: c, Agent: agent, Effort: effort, Skipped: skipped}, nil
	}
	var reasons []string
	for _, s := range skipped {
		reasons = append(reasons, s.Label+": "+s.Reason)
	}
	return selection{}, routerErr("no_eligible_candidate",
		"no preference candidate is eligible — "+strings.Join(reasons, "; "), http.StatusServiceUnavailable)
}

// candidateGate evaluates one preference candidate: every forwarded fact must
// be verified, a live agent must exist, and quota must pace. Returns the
// resolved agent and effort, or the explicit skip reason.
func candidateGate(c candidate, live []herdrAgent, reqEffort string, quota map[string]providerQuota, now time.Time, approved bool) (herdrAgent, string, string) {
	if !c.PolicyOK {
		return herdrAgent{}, "", "policy_excluded"
	}
	if c.Subscription == "" {
		return herdrAgent{}, "", "subscription_unverified"
	}
	if c.Model == "" {
		return herdrAgent{}, "", "model_unverified"
	}
	effort := c.Effort
	if reqEffort != "" {
		if c.EffortOpaque {
			return herdrAgent{}, "", "effort_unverified"
		}
		if !containsString(c.Efforts, reqEffort) {
			return herdrAgent{}, "", "effort_unsupported"
		}
		effort = reqEffort
	}
	agent, ok := liveAgentFor(live, c.Kind)
	if !ok {
		return herdrAgent{}, "", "agent_unavailable"
	}
	if gate := quotaGate(c.Subscription, quota, now, approved); gate != "" {
		return herdrAgent{}, "", gate
	}
	return agent, effort, ""
}

func liveAgentFor(live []herdrAgent, kind string) (herdrAgent, bool) {
	for _, a := range live {
		if a.Name == kind || a.PaneID == kind {
			return a, true
		}
	}
	return herdrAgent{}, false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// normalizeEffort clamps the extended client ladder onto the verified wire
// vocabulary: minimal maps to low and ultra to max, matching pi's
// thinkingLevelMap edge normalization for "cpa router".
func normalizeEffort(e string) string {
	switch e {
	case "minimal":
		return "low"
	case "ultra":
		return "max"
	default:
		return e
	}
}

// requestedEffort reads the client-requested reasoning effort from the
// openai payload (reasoning_effort, or a reasoning.effort object).
func requestedEffort(payload []byte) string {
	var doc map[string]any
	if json.Unmarshal(payload, &doc) != nil || doc == nil {
		return ""
	}
	if e, ok := doc["reasoning_effort"].(string); ok {
		return e
	}
	if r, ok := doc["reasoning"].(map[string]any); ok {
		if e, ok := r["effort"].(string); ok {
			return e
		}
	}
	return ""
}
