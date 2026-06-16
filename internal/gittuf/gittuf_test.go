package gittuf

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v6"
)

// TestRepo_Smoke exercises the experimental/gittuf wrapper methods against a
// bare repo with no policy. Most return errors (no metadata); the point is to
// cover the wrapper paths and the panic-recover guards. The full happy paths
// are covered by the testscript suite.
func TestRepo_Smoke(t *testing.T) {
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if r.HasSignedRoot() {
		t.Error("HasSignedRoot on empty repo should be false")
	}
	if tip := r.RSLTip(); tip != "" {
		t.Errorf("RSLTip = %q, want empty", tip)
	}
	if err := r.VerifyRef(ctx, "refs/heads/main"); err == nil {
		t.Error("VerifyRef on empty repo should error")
	}
	if _, err := r.VerifyMergeable(ctx, "main", "feature"); err == nil {
		t.Error("VerifyMergeable on empty repo should error")
	}
	if _, err := r.RuleFor(ctx, "refs/heads/main"); err == nil {
		t.Error("RuleFor with no policy should error")
	}
	if _, err := r.Policy(ctx); err == nil {
		t.Error("Policy with no metadata should error")
	}
	if _, err := r.Hooks(ctx); err == nil {
		t.Error("Hooks with no policy should error")
	}
	if err := r.Witness(ctx, "", "msg", nil); err != nil {
		t.Errorf("Witness with empty entryID should be a no-op, got %v", err)
	}
	if _, err := Open("/nonexistent/path"); err == nil {
		t.Error("Open on missing path should error")
	}
}

func TestIsGittufRef(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"refs/gittuf/policy", true},
		{"refs/gittuf/reference-state-log", true},
		{"refs/heads/main", false},
		{"refs/tags/v1", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsGittufRef(tc.in); got != tc.want {
			t.Errorf("IsGittufRef(%q) = %v", tc.in, got)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"git:refs/heads/main", "git:refs/heads/main", true},
		{"git:refs/heads/*", "git:refs/heads/main", true},
		{"git:refs/heads/*", "git:refs/heads/feature/x", true},
		{"git:refs/tags/*", "git:refs/heads/main", false},
		{"file:src/*", "file:src/crypto/x.go", true},
		{"git:refs/heads/main", "git:refs/heads/other", false},
	}
	for _, tc := range tests {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
