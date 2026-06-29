// Package jobs runs a background worker that drains store.jobs rows. Each
// kind has a registered handler; the worker enforces per-job timeout and
// recover so a panicking handler cannot kill the serve process.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/git-pkgs/silo/internal/store"
)

// Handler runs one job to completion. A non-nil return marks the job failed.
type Handler func(ctx context.Context, j store.Job) error

// Worker polls store.jobs and dispatches by kind. Nudge speeds the next poll
// (used by PostReceive to react instantly after enqueueing).
type Worker struct {
	Store    *store.Store
	Handlers map[string]Handler

	// Timeout caps the per-job context. 0 falls back to DefaultTimeout.
	Timeout time.Duration

	// PollInterval is the steady-state poll cadence between Nudge events.
	PollInterval time.Duration

	nudge chan struct{}
}

const (
	DefaultTimeout      = 10 * time.Minute
	DefaultPollInterval = 250 * time.Millisecond
)

// New constructs a Worker with sensible defaults.
func New(st *store.Store) *Worker {
	return &Worker{
		Store:        st,
		Handlers:     map[string]Handler{},
		Timeout:      DefaultTimeout,
		PollInterval: DefaultPollInterval,
		nudge:        make(chan struct{}, 1),
	}
}

// Register adds a handler for a kind. Replaces any prior handler.
func (w *Worker) Register(kind string, h Handler) {
	if w.Handlers == nil {
		w.Handlers = map[string]Handler{}
	}
	w.Handlers[kind] = h
}

// Nudge asks the worker to wake immediately and try a claim. Coalesces; safe
// to call from many goroutines.
func (w *Worker) Nudge() {
	if w.nudge == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run drives the worker until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if w.nudge == nil {
		w.nudge = make(chan struct{}, 1)
	}
	if w.PollInterval == 0 {
		w.PollInterval = DefaultPollInterval
	}
	if w.Timeout == 0 {
		w.Timeout = DefaultTimeout
	}

	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()

	for {
		// Drain whatever is claimable right now.
		for {
			j, err := w.claimAny()
			if err != nil {
				slog.Warn("jobs: claim", "err", err)
				break
			}
			if j == nil {
				break
			}
			w.run(ctx, *j)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
		}
	}
}

func (w *Worker) claimAny() (*store.Job, error) {
	kinds := make([]string, 0, len(w.Handlers))
	for k := range w.Handlers {
		kinds = append(kinds, k)
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	return w.Store.ClaimJob(kinds...)
}

func (w *Worker) run(parent context.Context, j store.Job) {
	h, ok := w.Handlers[j.Kind]
	if !ok {
		_ = w.Store.CompleteJob(j.ID, store.JobFailed)
		return
	}

	ctx, cancel := context.WithTimeout(parent, w.Timeout)
	defer cancel()

	err := safeCall(ctx, h, j)
	state := store.JobDone
	if err != nil {
		state = store.JobFailed
		if j.Attempts < store.MaxJobAttempts {
			state = store.JobPending
		}
		slog.Warn("jobs: handler failed", "kind", j.Kind, "id", j.ID, "attempt", j.Attempts, "err", err)
	}
	if cerr := w.Store.CompleteJob(j.ID, state); cerr != nil {
		slog.Warn("jobs: complete", "id", j.ID, "err", cerr)
	}
}

func safeCall(ctx context.Context, h Handler, j store.Job) (rerr error) {
	defer func() {
		if r := recover(); r != nil {
			rerr = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(ctx, j)
}
