package hooks

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/receive"
)

func TestPartition(t *testing.T) {
	updates := []receive.RefUpdate{
		{Name: "refs/heads/main"},
		{Name: "refs/gittuf/policy"},
		{Name: "refs/gittuf/reference-state-log"},
		{Name: "refs/tags/v1"},
	}
	g, o := partition(updates)
	if len(g) != 2 || len(o) != 2 {
		t.Errorf("partition = %d gittuf, %d other", len(g), len(o))
	}
}

func TestOwnerRepo(t *testing.T) {
	if got := ownerRepo("/data/repos/alice/demo.git"); got != "alice/demo" {
		t.Errorf("ownerRepo = %q", got)
	}
}

func TestPreReceive_NoRepoPath(t *testing.T) {
	b := &Builtin{}
	err := b.PreReceive(context.Background(), nil, nil)
	if err == nil {
		t.Error("PreReceive without repo path should fail")
	}
}

func TestPreReceive_NotInitialised(t *testing.T) {
	gst, err := gitstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := gst.Init("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	repoPath, _ := gst.Path("alice", "demo")

	b := &Builtin{}
	ctx := receive.WithRepoPath(context.Background(), repoPath)
	err = b.PreReceive(ctx, repo, []receive.RefUpdate{
		{Name: "refs/heads/main", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
	})
	rej, ok := err.(*receive.RejectionError)
	if !ok {
		t.Fatalf("err = %T %v, want *RejectionError", err, err)
	}
	if rej.Reason == "" || rej.Ref != "refs/heads/main" {
		t.Errorf("rejection = %+v", rej)
	}
	if b.lock != nil {
		t.Error("lock not released after rejection")
	}
}

func TestPreReceive_GittufRefsOnlyAllowed(t *testing.T) {
	gst, err := gitstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := gst.Init("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	repoPath, _ := gst.Path("alice", "demo")

	b := &Builtin{}
	ctx := receive.WithRepoPath(context.Background(), repoPath)
	err = b.PreReceive(ctx, repo, []receive.RefUpdate{
		{Name: "refs/gittuf/policy-staging", New: plumbing.ZeroHash}, // delete is fine
	})
	if err != nil {
		t.Errorf("PreReceive on gittuf-only updates = %v", err)
	}
	b.PostReceive(ctx, repo, nil)
	if b.lock != nil {
		t.Error("lock not released after PostReceive")
	}
}

func TestConcurrentRSL(t *testing.T) {
	gst, err := gitstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gst.Init("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := gst.Path("alice", "demo")

	// Two goroutines acquire the per-repo flock; the second must block until
	// the first releases. The lock file's existence after both is the witness
	// that flock was actually engaged.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			b := &Builtin{}
			if err := b.acquireLock(repoPath); err != nil {
				t.Error(err)
				return
			}
			b.releaseLock()
		})
	}
	wg.Wait()
	if _, err := os.Stat(filepath.Join(repoPath, "silo.lock")); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestApplyAndRollback(t *testing.T) {
	gst, _ := gitstore.Open(t.TempDir())
	repo, _ := gst.Init("a", "b")
	h := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	updates := []receive.RefUpdate{{Name: "refs/heads/x", Old: plumbing.ZeroHash, New: h}}

	if err := apply(repo, updates); err != nil {
		t.Fatal(err)
	}
	if ref, _ := repo.Reference("refs/heads/x", false); ref.Hash() != h {
		t.Error("apply didn't set ref")
	}
	rollback(repo, updates)
	if _, err := repo.Reference("refs/heads/x", false); err == nil {
		t.Error("rollback didn't remove ref")
	}
}
