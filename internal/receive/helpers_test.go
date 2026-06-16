package receive

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/storage/memory"
)

const testDefaultBranch = "refs/heads/main"

func newBareRepo(t testing.TB) *git.Repository {
	t.Helper()
	repo, err := git.Init(memory.NewStorage(), git.WithDefaultBranch(testDefaultBranch))
	if err != nil {
		t.Fatalf("init bare: %v", err)
	}
	return repo
}

// newSourceCommit creates an in-memory repo with one commit on main and
// returns the repo and commit hash.
func newSourceCommit(t testing.TB) (*git.Repository, plumbing.Hash) {
	t.Helper()
	st := memory.NewStorage()
	fs := memfs.New()
	repo, err := git.Init(st, git.WithDefaultBranch(testDefaultBranch), git.WithWorkTree(fs))
	if err != nil {
		t.Fatalf("init source: %v", err)
	}
	cfg, _ := repo.Config()
	cfg.Commit.GpgSign = config.NewOptBool(false)
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	f, err := fs.Create("README.md")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sig := &object.Signature{Name: "test", Email: "test@test", When: time.Unix(0, 0).UTC()}
	h, err := wt.Commit("one", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return repo, h
}

// encodePush builds the wire bytes a client would send for the given commands,
// with a packfile containing every object reachable from each non-zero New.
func encodePush(t testing.TB, src storer.Storer, cmds []*packp.Command, caps ...capability.Capability) *bytes.Buffer {
	t.Helper()
	req := &packp.UpdateRequests{Commands: cmds}
	for _, c := range caps {
		req.Capabilities.Add(c)
	}

	var buf bytes.Buffer
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode req: %v", err)
	}
	for _, c := range cmds {
		if !c.New.IsZero() {
			_, _ = buildPack(t, src, cmds).WriteTo(&buf)
			break
		}
	}
	return &buf
}

func buildPack(t testing.TB, src storer.Storer, cmds []*packp.Command) *bytes.Buffer {
	t.Helper()
	var hashes []plumbing.Hash
	seen := map[plumbing.Hash]bool{}
	for _, c := range cmds {
		if c.New.IsZero() {
			continue
		}
		walkObjects(t, src, c.New, seen, &hashes)
	}
	var pack bytes.Buffer
	enc := packfile.NewEncoder(&pack, src, false)
	if _, err := enc.Encode(hashes, 0); err != nil {
		t.Fatalf("encode pack: %v", err)
	}
	return &pack
}

func walkObjects(t testing.TB, st storer.EncodedObjectStorer, h plumbing.Hash, seen map[plumbing.Hash]bool, out *[]plumbing.Hash) {
	t.Helper()
	if h.IsZero() || seen[h] {
		return
	}
	seen[h] = true
	*out = append(*out, h)

	commit, err := object.GetCommit(st, h)
	if err != nil {
		return
	}
	walkObjects(t, st, commit.TreeHash, seen, out)
	for _, p := range commit.ParentHashes {
		walkObjects(t, st, p, seen, out)
	}
	tree, err := object.GetTree(st, commit.TreeHash)
	if err != nil {
		return
	}
	for _, e := range tree.Entries {
		walkObjects(t, st, e.Hash, seen, out)
	}
}

func decodeReport(t testing.TB, b []byte) *packp.ReportStatus {
	t.Helper()
	rs := &packp.ReportStatus{}
	if err := rs.Decode(bytes.NewReader(b)); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return rs
}
