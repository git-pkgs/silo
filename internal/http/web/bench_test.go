package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	gtw "github.com/git-pkgs/silo/internal/gittuf"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/store"
)

// benchFixture builds a repo with nCommits on main and one RSL reference
// entry per commit, so WalkRSL/verify scale with history length. The policy
// ref is a single fake metadata commit (gittuf can't parse it; handlers fall
// back to empty Policy, which is the cheap path — measuring the gittuf
// verifier itself needs a real policy and is covered by the txtar suite).
func benchFixture(b *testing.B, nCommits int) (*httptest.Server, string) {
	b.Helper()
	dir := b.TempDir()
	st, _ := store.Open(dir)
	b.Cleanup(func() { _ = st.Close() })
	gst, _ := gitstore.Open(dir)
	_, _ = st.CreateRepo("bench", "repo")
	repo, err := gst.Init("bench", "repo")
	if err != nil {
		b.Fatal(err)
	}
	seedSized(b, repo, nCommits)
	srv := httptest.NewServer(Handler(st, gst, "http://bench", "SHA256:forge"))
	b.Cleanup(srv.Close)
	return srv, "/bench/repo"
}

func seedSized(b *testing.B, repo *git.Repository, n int) {
	b.Helper()
	stor := repo.Storer
	sig := object.Signature{Name: "alice", Email: "a@x", When: time.Unix(1_700_000_000, 0)}

	var tip, rslTip plumbing.Hash
	emptyTree := writeTree(b, stor, nil)
	for i := range n {
		bh := writeBlob(b, stor, fmt.Sprintf("# repo\n\nline %d\n", i))
		th := writeTree(b, stor, []object.TreeEntry{
			{Name: "README.md", Mode: 0o100644, Hash: bh},
			{Name: "src", Mode: 0o40000, Hash: emptyTree},
		})
		c := &object.Commit{TreeHash: th, Author: sig, Committer: sig, Message: fmt.Sprintf("commit %d\n", i)}
		if !tip.IsZero() {
			c.ParentHashes = []plumbing.Hash{tip}
		}
		tip = writeCommit(b, stor, c)

		rc := &object.Commit{TreeHash: emptyTree, Committer: sig,
			Message: fmt.Sprintf("RSL Reference Entry\n\nref: refs/heads/main\ntargetID: %s\nnumber: %d\n", tip, i+1)}
		if !rslTip.IsZero() {
			rc.ParentHashes = []plumbing.Hash{rslTip}
		}
		rslTip = writeCommit(b, stor, rc)
	}
	setRef(b, stor, "refs/heads/main", tip)
	setRef(b, stor, "refs/heads/dev", tip)
	setRef(b, stor, "refs/tags/v1", tip)
	setRef(b, stor, gtw.RSLRef, rslTip)

	pj := writeBlob(b, stor, `{"fake":"policy"}`)
	mt := writeTree(b, stor, []object.TreeEntry{{Name: "targets.json", Mode: 0o100644, Hash: pj}})
	pt := writeTree(b, stor, []object.TreeEntry{{Name: "metadata", Mode: 0o40000, Hash: mt}})
	pc := writeCommit(b, stor, &object.Commit{TreeHash: pt, Author: sig, Committer: sig, Message: "policy\n"})
	setRef(b, stor, gtw.PolicyRef, pc)
}

func benchGet(b *testing.B, srv *httptest.Server, path string) {
	b.Helper()
	url := srv.URL + path
	resp, err := srv.Client().Get(url)
	if err != nil {
		b.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		b.Fatalf("GET %s = %d: %s", path, resp.StatusCode, body)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	b.ResetTimer()
	for b.Loop() {
		resp, err := srv.Client().Get(url)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

const benchN = 200

func BenchmarkWeb_Index(b *testing.B)   { srv, _ := benchFixture(b, benchN); benchGet(b, srv, "/") }
func BenchmarkWeb_Activity(b *testing.B) {
	srv, _ := benchFixture(b, benchN)
	benchGet(b, srv, "/activity")
}
func BenchmarkWeb_Repo(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p) }
func BenchmarkWeb_Tree(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/tree/refs/heads/main") }
func BenchmarkWeb_Blob(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/blob/refs/heads/main/README.md") }
func BenchmarkWeb_Log(b *testing.B)    { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/log/refs/heads/main") }
func BenchmarkWeb_LogJSON(b *testing.B) {
	srv, p := benchFixture(b, benchN)
	benchGet(b, srv, p+"/log/refs/heads/main?format=json")
}
func BenchmarkWeb_RSL(b *testing.B)      { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/rsl") }
func BenchmarkWeb_RSLRef(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/rsl/refs/heads/main") }
func BenchmarkWeb_Policy(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/policy") }
func BenchmarkWeb_PolicyHist(b *testing.B) {
	srv, p := benchFixture(b, benchN)
	benchGet(b, srv, p+"/policy/history")
}
func BenchmarkWeb_Verify(b *testing.B)   { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/verify") }
func BenchmarkWeb_Branches(b *testing.B) { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/branches") }
func BenchmarkWeb_Tags(b *testing.B)     { srv, p := benchFixture(b, benchN); benchGet(b, srv, p+"/tags") }
func BenchmarkWeb_Compare(b *testing.B) {
	srv, p := benchFixture(b, benchN)
	benchGet(b, srv, p+"/compare/refs/heads/main...refs/heads/dev")
}
func BenchmarkWeb_Contributors(b *testing.B) {
	srv, p := benchFixture(b, benchN)
	benchGet(b, srv, p+"/contributors")
}
