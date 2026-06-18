package hooks

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/receive"
	"github.com/git-pkgs/silo/internal/store"
)

func TestPostReceive_EnqueuesReindex(t *testing.T) {
	dataDir := t.TempDir()
	gst, err := gitstore.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gst.Init("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	dbRepo, err := st.CreateRepo("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}

	repoPath, _ := gst.Path("alice", "demo")
	ctx := receive.WithRepoPath(context.Background(), repoPath)

	nudged := 0
	b := &Builtin{
		Store: st,
		Nudge: func() { nudged++ },
	}
	// Take the lock so PostReceive's defer releaseLock can run safely.
	if err := b.acquireLock(repoPath); err != nil {
		t.Fatal(err)
	}

	b.PostReceive(ctx, nil, []receive.RefUpdate{
		{
			Name: plumbing.ReferenceName("refs/heads/main"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		},
		{
			// tag updates should not enqueue
			Name: plumbing.ReferenceName("refs/tags/v1"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
	})

	jobs, err := st.JobsForRepo(dbRepo.ID, "pkgs-reindex")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d: %#v", len(jobs), jobs)
	}
	if nudged != 1 {
		t.Errorf("want 1 nudge, got %d", nudged)
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	owner, repo, ok := splitOwnerRepo("/data/repos/alice/demo.git")
	if !ok || owner != "alice" || repo != "demo" {
		t.Errorf("splitOwnerRepo = %q,%q,%v", owner, repo, ok)
	}
	if _, _, ok := splitOwnerRepo("/data/repos/alice/demo"); ok {
		t.Errorf("expected !ok for missing .git")
	}
}
