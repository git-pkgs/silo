package gitstore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

func TestPath(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		owner, name string
		ok          bool
	}{
		{"alice", "demo", true},
		{"a-b_c.d", "Repo123", true},
		{"", "x", false},
		{"x", "", false},
		{"../etc", "x", false},
		{"x", "y/z", false},
		{"x", "..", false},
		{".x", "y", false},
	}
	for _, tc := range tests {
		p, err := s.Path(tc.owner, tc.name)
		if tc.ok {
			if err != nil {
				t.Errorf("Path(%q,%q) error: %v", tc.owner, tc.name, err)
			}
			if filepath.Base(p) != tc.name+".git" {
				t.Errorf("Path(%q,%q) = %q", tc.owner, tc.name, p)
			}
		} else if !errors.Is(err, ErrInvalidName) {
			t.Errorf("Path(%q,%q) = %q, %v; want ErrInvalidName", tc.owner, tc.name, p, err)
		}
	}
}

func TestInitAndRepo(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Repo("alice", "demo"); !errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Errorf("Repo before Init: %v, want ErrRepositoryNotFound", err)
	}

	repo, err := s.Init("alice", "demo")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	head, err := repo.Reference("HEAD", false)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if head.Target() != "refs/heads/main" {
		t.Errorf("HEAD target = %q, want refs/heads/main", head.Target())
	}

	if _, err := s.Init("alice", "demo"); err == nil {
		t.Error("Init on existing repo should fail")
	}

	if _, err := s.Repo("alice", "demo"); err != nil {
		t.Errorf("Repo after Init: %v", err)
	}

	if _, err := s.Repo("../x", "y"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Repo with bad name: %v", err)
	}
	if _, err := s.Init("../x", "y"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Init with bad name: %v", err)
	}
}
