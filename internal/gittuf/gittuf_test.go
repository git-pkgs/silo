package gittuf

import "testing"

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
