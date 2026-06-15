package receive

import (
	"fmt"
	"strings"
)

// RejectionError is returned from Hooks.PreReceive to refuse a push with a
// structured explanation. Sideband produces the multi-line progress output;
// Status produces the per-ref report-status reason.
type RejectionError struct {
	Ref        string
	Rule       string
	Threshold  int
	Principals []string
	Pusher     string
	PusherKey  string
	InSet      bool
	Approvals  int
	PolicyURL  string
	Reason     string
}

func (e *RejectionError) Error() string {
	if e.Rule != "" {
		return fmt.Sprintf("rejected %s: rule %q unsatisfied", e.Ref, e.Rule)
	}
	return "rejected " + e.Ref + ": " + e.Reason
}

// StatusPolicy is the report-status reason for a gittuf policy refusal.
const StatusPolicy = "policy"

// Status returns the short reason placed in the report-status `ng` line.
func (e *RejectionError) Status() string {
	if e.Reason != "" {
		return e.Reason
	}
	return StatusPolicy
}

// Sideband returns the human-readable lines written to the progress channel,
// one fact per line, for the client's `remote:` output.
func (e *RejectionError) Sideband() []string {
	lines := []string{"silo: rejected " + e.Ref}
	if e.Rule != "" {
		lines = append(lines, fmt.Sprintf("  rule '%s' requires %d of: %s",
			e.Rule, e.Threshold, strings.Join(e.Principals, ", ")))
	}
	if e.Pusher != "" {
		who := "not in principal set"
		if e.InSet {
			who = "in set"
		}
		lines = append(lines, fmt.Sprintf("  you pushed as: %s (%s) — %s", e.Pusher, e.PusherKey, who))
	}
	if e.Threshold > 0 {
		lines = append(lines, fmt.Sprintf("  approvals on record: %d/%d", e.Approvals, e.Threshold))
	}
	if e.PolicyURL != "" {
		lines = append(lines, "  policy: "+e.PolicyURL)
	}
	if e.Reason != "" && e.Rule == "" {
		lines = append(lines, "  "+e.Reason)
	}
	return lines
}
