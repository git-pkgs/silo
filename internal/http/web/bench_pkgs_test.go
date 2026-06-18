package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/store"
)

// makeLockfile returns a synthetic package-lock.json with n entries under
// the v3 "packages" map; the body is roughly five lines per entry, so n=1000
// produces ~5000 lines of source.
func makeLockfile(n int) string {
	var b strings.Builder
	b.WriteString(`{"name":"x","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n  \"node_modules/pkg%d\": {\n    \"version\": \"1.0.%d\",\n    \"resolved\": \"https://registry.npmjs.org/pkg%d/-/pkg%d-1.0.%d.tgz\",\n    \"integrity\": \"sha512-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa==\"\n  }", i, i, i, i, i)
	}
	b.WriteString("\n}}")
	return b.String()
}

// makeLockfileBumped returns the same shape with every version replaced by
// 1.0.<i>+1, so a Textconv diff classifies every entry as "updated".
func makeLockfileBumped(n int) string {
	var b strings.Builder
	b.WriteString(`{"name":"x","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n  \"node_modules/pkg%d\": {\n    \"version\": \"1.0.%d\",\n    \"resolved\": \"https://registry.npmjs.org/pkg%d/-/pkg%d-1.0.%d.tgz\",\n    \"integrity\": \"sha512-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb==\"\n  }", i, i+1, i, i, i+1)
	}
	b.WriteString("\n}}")
	return b.String()
}

// seedLockfileCommit puts a single commit on main whose only changed file is
// package-lock.json. The before/after pair has the requested entry count, so
// the commit-page render exercises the rich Textconv path on a 5000-line
// lockfile diff.
func seedLockfileCommit(b *testing.B, repo *git.Repository, entries int) (parent, head plumbing.Hash) {
	b.Helper()
	stor := repo.Storer
	sig := object.Signature{Name: "a", Email: "a@x", When: time.Unix(1_700_000_000, 0)}

	beforeBlob := writeBlob(b, stor, makeLockfile(entries))
	beforeTree := writeTree(b, stor, []object.TreeEntry{
		{Name: "package-lock.json", Mode: 0o100644, Hash: beforeBlob},
	})
	beforeC := writeCommit(b, stor, &object.Commit{TreeHash: beforeTree, Author: sig, Committer: sig, Message: "before\n"})

	afterBlob := writeBlob(b, stor, makeLockfileBumped(entries))
	afterTree := writeTree(b, stor, []object.TreeEntry{
		{Name: "package-lock.json", Mode: 0o100644, Hash: afterBlob},
	})
	afterC := writeCommit(b, stor, &object.Commit{TreeHash: afterTree, Author: sig, Committer: sig, Message: "bump all\n", ParentHashes: []plumbing.Hash{beforeC}})

	setRef(b, stor, "refs/heads/main", afterC)
	return beforeC, afterC
}

func benchLockfileFixture(b *testing.B, entries int) (*httptest.Server, string, string) {
	b.Helper()
	dir := b.TempDir()
	st, _ := store.Open(dir)
	b.Cleanup(func() { _ = st.Close() })
	gst, _ := gitstore.Open(dir)
	_, _ = st.CreateRepo("bench", "lock")
	repo, err := gst.Init("bench", "lock")
	if err != nil {
		b.Fatal(err)
	}
	_, head := seedLockfileCommit(b, repo, entries)
	srv := httptest.NewServer(Handler(st, gst, "http://bench", "SHA256:forge"))
	b.Cleanup(srv.Close)
	return srv, "/bench/lock", head.String()
}

// BenchmarkCommitPage_Lockfile times the rich render of a ~5000-line
// package-lock.json diff. The spec's done-when is 200 ms warm with 4×
// headroom (fail >800 ms).
func BenchmarkCommitPage_Lockfile(b *testing.B) {
	srv, repoPath, sha := benchLockfileFixture(b, 1000) // ~5000 lines
	url := srv.URL + repoPath + "/commit/" + sha

	// Warm cache.
	resp, err := srv.Client().Get(url)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("status %d", resp.StatusCode)
	}

	const limit = 800 * time.Millisecond

	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		resp, err := srv.Client().Get(url)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if d := time.Since(start); d > limit {
			b.Fatalf("commit page render took %v, want < %v", d, limit)
		}
	}
}
