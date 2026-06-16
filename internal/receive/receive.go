// Package receive adapts go-git's transport.ReceivePack to silo's hook
// interface, adding pack-size limits and the structured RejectionError
// sideband output.
package receive

import (
	"context"
	"errors"
	"io"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
)

// RefUpdate is a single ref change requested by the client.
type RefUpdate struct {
	Name     plumbing.ReferenceName
	Old, New plumbing.Hash
}

// IsDelete reports whether the update removes the ref.
func (u RefUpdate) IsDelete() bool { return u.New.IsZero() }

// Hooks is the callback contract for ReceivePack. PreReceive runs after the
// packfile is unpacked but before any ref is updated; returning a
// *RejectionError refuses every ref in the push with that error's sideband
// message and report-status reason. PostReceive runs after refs are applied
// and is best-effort.
type Hooks interface {
	PreReceive(ctx context.Context, repo *git.Repository, updates []RefUpdate) error
	PostReceive(ctx context.Context, repo *git.Repository, updates []RefUpdate)
}

// NoopHooks accepts every push and does nothing afterwards.
type NoopHooks struct{}

func (NoopHooks) PreReceive(context.Context, *git.Repository, []RefUpdate) error { return nil }
func (NoopHooks) PostReceive(context.Context, *git.Repository, []RefUpdate)      {}

// Limits bounds the resources a single push may consume.
type Limits struct {
	MaxPackBytes int64
	MaxObjects   uint32
}

// DefaultLimits returns the limits applied when none are specified.
func DefaultLimits() Limits {
	const (
		maxPackBytes = 512 << 20
		maxObjects   = 100_000
	)
	return Limits{MaxPackBytes: maxPackBytes, MaxObjects: maxObjects}
}

// ReceivePack runs the full server-side receive-pack exchange (advertise,
// decode, unpack, hooks, ref update, report-status) over r and w. The reader
// is bounded by limits.MaxPackBytes; MaxObjects is not enforced at this layer
// once go-git owns unpack.
func ReceivePack(ctx context.Context, repo *git.Repository, r io.Reader, w io.Writer, hooks Hooks, limits Limits) error {
	if hooks == nil {
		hooks = NoopHooks{}
	}
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}

	lr := io.NopCloser(newCapReader(r, limits.MaxPackBytes))
	wc := nopWriteCloser{w}

	err := transport.ReceivePack(ctx, repo.Storer, lr, wc, &transport.ReceivePackRequest{
		Hooks: adapt(repo, hooks),
	})
	if errors.Is(err, errPackTooLarge) || errors.Is(err, errHookRejected) {
		return nil
	}
	return err
}

var errHookRejected = errors.New("pre-receive declined")

func adapt(repo *git.Repository, h Hooks) *transport.ReceivePackHooks {
	return &transport.ReceivePackHooks{
		PreReceive: func(ctx context.Context, _ storage.Storer, cmds []*packp.Command, progress io.Writer) error {
			updates := toUpdates(cmds)
			err := h.PreReceive(ctx, repo, updates)
			if err == nil {
				return nil
			}
			if rej, ok := errors.AsType[*RejectionError](err); ok {
				for _, ln := range rej.Sideband() {
					_, _ = io.WriteString(progress, ln+"\n")
				}
				return errors.Join(errHookRejected, &statusErr{rej.Status()})
			}
			_, _ = io.WriteString(progress, "silo: pre-receive: "+err.Error()+"\n")
			return errors.Join(errHookRejected, &statusErr{"pre-receive declined"})
		},
		PostReceive: func(ctx context.Context, _ storage.Storer, cmds []*packp.Command) {
			h.PostReceive(ctx, repo, toUpdates(cmds))
		},
	}
}

type statusErr struct{ s string }

func (e *statusErr) Error() string { return e.s }

func toUpdates(cmds []*packp.Command) []RefUpdate {
	out := make([]RefUpdate, len(cmds))
	for i, c := range cmds {
		out[i] = RefUpdate{Name: c.Name, Old: c.Old, New: c.New}
	}
	return out
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
