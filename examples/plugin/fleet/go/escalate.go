// escalate.go: conditional read-only sidekick. The structure follows Cognition's
// Devin Fusion findings — a lead owns the session (plan, ambiguity, review)
// while a second agent with its own persistent context works alongside it
// (https://cognition.com/blog/devin-fusion,
// https://cognition.com/blog/local-fusion). One deliberate inversion: Fusion's
// sidekick executes delegated work; issue #12 requires read-only scrutiny
// instead, so the sidekick never receives a mutation channel — its text
// opinion is relayed into the lead's finalize turn and cannot reach the
// client or trigger writes through this adapter.
package main

import (
	"fmt"
	"strings"
)

const (
	roleLead     = "lead"
	roleSidekick = "sidekick"
	roleFinalize = "lead_finalize"
	verifyMarker = "CPA-VERIFY:"
)

// escalationTriggers names why a request needs independent scrutiny: the
// client-declared difficulty or importance, or the lead reporting a failed
// verification inside its framed output.
func escalationTriggers(req *executorCallRequest, res leadResult) []string {
	var triggers []string
	for _, v := range req.Headers["x-fleet-difficulty"] {
		if highSignal(v) {
			triggers = append(triggers, "difficulty")
		}
	}
	for _, v := range req.Headers["x-fleet-importance"] {
		if highSignal(v) {
			triggers = append(triggers, "importance")
		}
	}
	if res.Verification == "failed" {
		triggers = append(triggers, "verification_failed")
	}
	return triggers
}

func highSignal(v string) bool {
	return v == "high" || v == "critical"
}

// sidekickBrief is the read-only review task. The sidekick sees the original
// task and the lead's draft; it has no write path and no client visibility.
func sidekickBrief(task, draft string) string {
	return fmt.Sprintf("Independently review this draft answer for correctness, missed risks, and unsafe assumptions. Report only findings, or exactly \"no findings\".\n\nTASK:\n%s\n\nLEAD DRAFT:\n%s", task, draft)
}

// finalizeBrief hands the review back to the lead, which retains ownership:
// it incorporates or rejects findings and emits the only client-visible text.
func finalizeBrief(task, draft, review string) string {
	return fmt.Sprintf("An independent read-only review of your draft follows. You retain ownership: incorporate valid findings, reject invalid ones, then emit the final answer.\n\nTASK:\n%s\n\nYOUR DRAFT:\n%s\n\nREVIEW:\n%s", task, draft, review)
}

// splitVerification peels the trailing CPA-VERIFY:<ok|failed> line the prompt
// contract asks for. Anything else is treated as unreported, not failure.
func splitVerification(text string) (string, string) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, verifyMarker) {
			return text, ""
		}
		v := strings.TrimSpace(strings.TrimPrefix(l, verifyMarker))
		rest := strings.TrimSpace(strings.Join(lines[:i], "\n"))
		if v != "ok" && v != "failed" {
			return rest, ""
		}
		return rest, v
	}
	return text, ""
}
