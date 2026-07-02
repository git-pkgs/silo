package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v6"
	plumbing6 "github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/pkgs"
	"github.com/git-pkgs/silo/internal/store"
)

const fixtureGoMod1 = `module example.com/demo

go 1.26

require (
	github.com/spf13/cobra v1.8.0
	github.com/stretchr/testify v1.9.0
)
`

const fixtureGoMod2 = `module example.com/demo

go 1.26

require (
	github.com/spf13/cobra v1.9.0
	github.com/stretchr/testify v1.9.0
)
`

// seedDepsFixture is like seedCommit but writes a go.mod across two commits
// so the pkgs index has something to list/blame/diff. Returns the two commit
// SHAs.
func seedDepsFixture(t *testing.T, repo *git.Repository) (sha1, sha2 string) {
	t.Helper()
	st := repo.Storer
	sig := object.Signature{Name: "alice", Email: "a@x", When: time.Unix(1_700_000_000, 0)}

	b1 := writeBlob(t, st, fixtureGoMod1)
	t1 := writeTree(t, st, []object.TreeEntry{{Name: "go.mod", Mode: 0o100644, Hash: b1}})
	c1 := writeCommit(t, st, &object.Commit{TreeHash: t1, Author: sig, Committer: sig, Message: "add deps\n"})

	b2 := writeBlob(t, st, fixtureGoMod2)
	t2 := writeTree(t, st, []object.TreeEntry{{Name: "go.mod", Mode: 0o100644, Hash: b2}})
	c2 := writeCommit(t, st, &object.Commit{TreeHash: t2, ParentHashes: []plumbing6.Hash{c1}, Author: sig, Committer: sig, Message: "bump cobra\n"})
	setRef(t, st, "refs/heads/main", c2)

	// Minimal gittuf refs so h.page doesn't choke.
	pj := writeBlob(t, st, `{"fake":"policy"}`)
	mt := writeTree(t, st, []object.TreeEntry{{Name: "targets.json", Mode: 0o100644, Hash: pj}})
	pt := writeTree(t, st, []object.TreeEntry{{Name: "metadata", Mode: 0o40000, Hash: mt}})
	pc := writeCommit(t, st, &object.Commit{TreeHash: pt, Author: sig, Committer: sig, Message: "Initialize policy\n"})
	setRef(t, st, "refs/gittuf/policy", pc)
	r1 := writeCommit(t, st, &object.Commit{TreeHash: t1, Committer: sig,
		Message: "RSL Reference Entry\n\nref: refs/heads/main\ntargetID: " + c2.String() + "\nnumber: 1\n"})
	setRef(t, st, "refs/gittuf/reference-state-log", r1)

	return c1.String(), c2.String()
}

func newDepsFixture(t *testing.T) (*httptest.Server, *store.Store, int64, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gst, err := gitstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	dbRepo, err := st.CreateRepo("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := gst.Init("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	sha1, sha2 := seedDepsFixture(t, repo)

	ps := pkgs.Open(index.Options{})
	t.Cleanup(ps.Close)
	if err := ps.Reindex(context.Background(), gst, "alice", "demo", "main",
		plumbing.ZeroHash, plumbing.NewHash(sha2)); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	srv := httptest.NewServer(Handler(st, gst, "http://silo.test", "SHA256:forge", WithPkgsStore(ps)))
	t.Cleanup(srv.Close)
	return srv, st, dbRepo.ID, sha1, sha2
}

func TestDependenciesPages(t *testing.T) {
	srv, _, _, _, _ := newDepsFixture(t)

	cases := []struct {
		path     string
		want     int
		contains string
	}{
		{"/alice/demo/dependencies", http.StatusOK, "github.com/spf13/cobra"},
		{"/alice/demo/dependencies?ref=main", http.StatusOK, "github.com/spf13/cobra"},
		{"/alice/demo/dependencies/blame", http.StatusOK, "github.com/spf13/cobra"},
		{"/alice/demo/dependencies/blame?ref=nope", http.StatusOK, ""},
		{"/alice/demo/dependencies/stats", http.StatusOK, "golang"},
		{"/alice/demo/dependencies/stats?ref=nope", http.StatusOK, "nope"},
		{"/alice/demo/dependencies/pkg:golang%2Fgithub.com%2Fspf13%2Fcobra@v1.9.0", http.StatusOK, "github.com/spf13/cobra"},
		{"/alice/demo/dependencies/pkg:golang%2Fgithub.com%2Fspf13%2Fcobra", http.StatusOK, "github.com/spf13/cobra"},
		{"/nobody/nothing/dependencies", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := srv.Client().Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d; body:\n%s", resp.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusOK {
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("Content-Type = %q, want text/html", ct)
				}
			}
			if tc.contains != "" && !strings.Contains(string(body), tc.contains) {
				t.Errorf("body does not contain %q:\n%s", tc.contains, body)
			}
		})
	}
}

func TestDependenciesPages_Indexing(t *testing.T) {
	srv, st, repoID, _, _ := newDepsFixture(t)
	if _, err := st.EnqueueJob(repoID, pkgs.JobKind, `{}`); err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Get(srv.URL + "/alice/demo/dependencies")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(strings.ToLower(string(body)), "indexing") {
		t.Errorf("expected indexing notice in body:\n%s", body)
	}
}

func TestPurlPackageName(t *testing.T) {
	cases := map[string]string{
		"pkg:golang/github.com/spf13/cobra@v1.9.0": "github.com/spf13/cobra",
		"pkg:golang/github.com/spf13/cobra":        "github.com/spf13/cobra",
		"pkg:npm/lodash@4.17.21":                   "lodash",
		"pkg:npm/%40babel/core@7.24.0":             "@babel/core",
		"pkg:maven/org.junit/junit@4.13":           "org.junit:junit",
		"not-a-purl":                               "not-a-purl",
		"pkg:":                                     "pkg:",
	}
	for in, want := range cases {
		if got := purlPackageName(in); got != want {
			t.Errorf("purlPackageName(%q) = %q, want %q", in, got, want)
		}
	}
}
