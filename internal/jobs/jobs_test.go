package jobs_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/git-pkgs/silo/internal/jobs"
	"github.com/git-pkgs/silo/internal/store"
)

func newStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r, err := s.CreateRepo("alice", "demo")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return s, r.ID
}

func TestWorker_RunHappy(t *testing.T) {
	s, repoID := newStore(t)
	if _, err := s.EnqueueJob(repoID, "test", `{}`); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	w := jobs.New(s)
	w.PollInterval = 10 * time.Millisecond
	w.Register("test", func(_ context.Context, _ store.Job) error {
		calls.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	w.Nudge()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler not called: %d", calls.Load())
	}

	// Wait for completion to be persisted.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		js, _ := s.JobsForRepo(repoID, "test")
		if len(js) == 1 && js[0].State == store.JobDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job never reached done state")
}

func TestWorker_Recover(t *testing.T) {
	s, repoID := newStore(t)
	if _, err := s.EnqueueJob(repoID, "test", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueJob(repoID, "test", `{"i":2}`); err != nil {
		t.Fatal(err)
	}

	var seenSecond atomic.Bool
	w := jobs.New(s)
	w.PollInterval = 10 * time.Millisecond
	w.Register("test", func(_ context.Context, j store.Job) error {
		if j.Payload == `{}` {
			panic("boom")
		}
		seenSecond.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	w.Nudge()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if seenSecond.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !seenSecond.Load() {
		t.Fatal("worker stopped after panic")
	}
}
