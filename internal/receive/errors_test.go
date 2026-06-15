package receive

import (
	"strings"
	"testing"
)

func TestRejectionError_Sideband(t *testing.T) {
	tests := []struct {
		name string
		rej  RejectionError
		want []string
	}{
		{
			name: "full policy rejection",
			rej: RejectionError{
				Ref:        "refs/heads/main",
				Rule:       "protect-main",
				Threshold:  2,
				Principals: []string{"alice", "bob", "carol"},
				Pusher:     "andrew",
				PusherKey:  "SHA256:tL3x",
				InSet:      false,
				Approvals:  0,
				PolicyURL:  "https://silo.example.com/andrew/demo/policy#protect-main",
			},
			want: []string{
				"silo: rejected refs/heads/main",
				"  rule 'protect-main' requires 2 of: alice, bob, carol",
				"  you pushed as: andrew (SHA256:tL3x) — not in principal set",
				"  approvals on record: 0/2",
				"  policy: https://silo.example.com/andrew/demo/policy#protect-main",
			},
		},
		{
			name: "pusher in set, partial approvals",
			rej: RejectionError{
				Ref:        "refs/heads/main",
				Rule:       "protect-main",
				Threshold:  3,
				Principals: []string{"alice", "bob", "carol"},
				Pusher:     "alice",
				PusherKey:  "SHA256:aaa",
				InSet:      true,
				Approvals:  1,
			},
			want: []string{
				"silo: rejected refs/heads/main",
				"  rule 'protect-main' requires 3 of: alice, bob, carol",
				"  you pushed as: alice (SHA256:aaa) — in set",
				"  approvals on record: 1/3",
			},
		},
		{
			name: "reason only (no policy)",
			rej: RejectionError{
				Ref:    "refs/heads/main",
				Reason: "repo not initialised: run `gittuf trust init` and push refs/gittuf/policy",
			},
			want: []string{
				"silo: rejected refs/heads/main",
				"  repo not initialised: run `gittuf trust init` and push refs/gittuf/policy",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rej.Sideband()
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("Sideband mismatch\ngot:\n%s\nwant:\n%s",
					strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestRejectionError_Status(t *testing.T) {
	if got := (&RejectionError{Rule: "x"}).Status(); got != "policy" {
		t.Errorf("Status with rule = %q, want policy", got)
	}
	if got := (&RejectionError{Reason: "uninitialised"}).Status(); got != "uninitialised" {
		t.Errorf("Status with reason = %q, want uninitialised", got)
	}
}

func TestRejectionError_Error(t *testing.T) {
	e := &RejectionError{Ref: "refs/heads/main", Rule: "r"}
	if !strings.Contains(e.Error(), "refs/heads/main") || !strings.Contains(e.Error(), "r") {
		t.Errorf("Error() = %q", e.Error())
	}
	e2 := &RejectionError{Ref: "refs/heads/x", Reason: "nope"}
	if !strings.Contains(e2.Error(), "nope") {
		t.Errorf("Error() = %q", e2.Error())
	}
}
