package store

import (
	"errors"
	"slices"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSchema(t *testing.T) {
	s := newStore(t)
	rows, err := s.DB().Query(`SELECT name FROM pragma_table_list WHERE schema='main' AND type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"users", "ssh_keys", "tokens", "repos", "repo_members", "jobs"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("schema missing table %q; have %v", w, got)
		}
	}
}

func TestUserBySSHFingerprint(t *testing.T) {
	s := newStore(t)
	u, err := s.CreateUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSSHKey(u.ID, "SHA256:abc", "ssh-ed25519 AAAA"); err != nil {
		t.Fatal(err)
	}

	got, err := s.UserBySSHFingerprint("SHA256:abc")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Name != "alice" || got.ID != u.ID {
		t.Errorf("got %+v", got)
	}

	if _, err := s.UserBySSHFingerprint("SHA256:nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key: %v, want ErrNotFound", err)
	}
}

func TestCreateUser_Duplicate(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateUser("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice"); err == nil {
		t.Error("duplicate user should fail")
	}
}

func TestRepoByPath(t *testing.T) {
	s := newStore(t)
	if _, err := s.RepoByPath("a", "b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo: %v", err)
	}
	r, err := s.CreateRepo("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RepoByPath("a", "b")
	if err != nil || got.ID != r.ID {
		t.Errorf("RepoByPath = %+v, %v", got, err)
	}
	if _, err := s.CreateRepo("a", "b"); err == nil {
		t.Error("duplicate repo should fail")
	}
}
