package store_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/git-pkgs/silo/internal/store"
)

func setup(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repo, err := s.CreateRepo("alice", "demo")
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return s, repo.ID
}

func TestEnqueueDedup(t *testing.T) {
	s, repoID := setup(t)
	id1, err := s.EnqueueJob(repoID, "pkgs-reindex", `{"branch":"main"}`)
	if err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	id2, err := s.EnqueueJob(repoID, "pkgs-reindex", `{"branch":"main"}`)
	if err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("dedup failed: %d != %d", id1, id2)
	}
	jobs, err := s.JobsForRepo(repoID, "pkgs-reindex")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
}

func TestClaimJob(t *testing.T) {
	s, repoID := setup(t)
	for i := 0; i < 5; i++ {
		// Distinct payloads so dedup doesn't merge them.
		payload := `{"i":` + string(rune('0'+i)) + `}`
		if _, err := s.EnqueueJob(repoID, "pkgs-reindex", payload); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var (
		wg      sync.WaitGroup
		claimed sync.Map
		dupes   atomic.Int32
	)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := s.ClaimJob("pkgs-reindex")
			if err != nil || j == nil {
				return
			}
			if _, dup := claimed.LoadOrStore(j.ID, true); dup {
				dupes.Add(1)
			}
		}()
	}
	wg.Wait()

	if dupes.Load() != 0 {
		t.Fatalf("dup claims: %d", dupes.Load())
	}
	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 5 {
		t.Fatalf("want 5 claimed, got %d", count)
	}
}

func TestClaimJob_RespectsAttemptCap(t *testing.T) {
	s, repoID := setup(t)
	if _, err := s.EnqueueJob(repoID, "pkgs-reindex", `{}`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxJobAttempts; i++ {
		j, err := s.ClaimJob("pkgs-reindex")
		if err != nil || j == nil {
			t.Fatalf("claim %d: %v %v", i, j, err)
		}
		// Mark failed but reset to pending so it can be re-claimed up to the cap.
		if _, err := s.DB().Exec(`UPDATE jobs SET state='pending' WHERE id=?`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.ClaimJob("pkgs-reindex")
	if err != nil {
		t.Fatalf("final claim: %v", err)
	}
	if j != nil {
		t.Fatalf("expected nil claim after attempt cap, got %+v", j)
	}
}

func TestCompleteJob(t *testing.T) {
	s, repoID := setup(t)
	id, err := s.EnqueueJob(repoID, "pkgs-reindex", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimJob("pkgs-reindex"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(id, store.JobDone); err != nil {
		t.Fatalf("complete: %v", err)
	}
	jobs, err := s.JobsForRepo(repoID, "pkgs-reindex")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != store.JobDone {
		t.Fatalf("want done state, got %+v", jobs)
	}

	pending, err := s.HasPendingOrRunning(repoID, "pkgs-reindex")
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatalf("HasPendingOrRunning should be false after CompleteJob")
	}
}
