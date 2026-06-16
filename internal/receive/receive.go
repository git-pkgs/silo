// Package receive implements the server side of git's receive-pack protocol
// over arbitrary io streams, with a hook callback between packfile unpack and
// ref application.
package receive

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/plumbing/storer"
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
// and is best-effort: errors are not surfaced to the client.
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

// Agent is advertised in the capability line and may be overridden at link
// time.
var Agent = "silo/dev"

const statusOK = "ok"

// Advertise writes the receive-pack ref advertisement for repo to w. The same
// advertisement serves an SSH session's initial write and a smart-HTTP
// info/refs response (the caller adds the HTTP service prefix via
// AdvRefs.Prefix if needed).
func Advertise(repo *git.Repository, w io.Writer) error {
	ar, err := buildAdvRefs(repo)
	if err != nil {
		return err
	}
	return ar.Encode(w)
}

func buildAdvRefs(repo *git.Repository) (*packp.AdvRefs, error) {
	ar := &packp.AdvRefs{}
	for _, c := range []capability.Capability{
		capability.ReportStatus,
		capability.Sideband64k,
		capability.DeleteRefs,
		capability.Quiet,
		capability.OFSDelta,
	} {
		ar.Capabilities.Add(c)
	}
	ar.Capabilities.Add(capability.Agent, Agent)

	iter, err := repo.Storer.IterReferences()
	if err != nil {
		return nil, err
	}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		ar.References = append(ar.References, ref)
		return nil
	}); err != nil {
		return nil, err
	}
	return ar, nil
}

// ReceivePack reads a reference-update request and optional packfile from r,
// runs hooks, applies ref updates, and writes report-status (and any sideband
// progress) to w. It returns nil when the protocol exchange completes, even if
// the push was refused; protocol or I/O failures return an error.
func ReceivePack(ctx context.Context, repo *git.Repository, r io.Reader, w io.Writer, hooks Hooks, limits Limits) error {
	if hooks == nil {
		hooks = NoopHooks{}
	}
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}

	rd := bufio.NewReader(r)
	req := &packp.UpdateRequests{}
	if err := req.Decode(rd); err != nil {
		return err
	}

	updates := make([]RefUpdate, 0, len(req.Commands))
	needPack := false
	for _, c := range req.Commands {
		updates = append(updates, RefUpdate{Name: c.Name, Old: c.Old, New: c.New})
		if !c.New.IsZero() {
			needPack = true
		}
	}

	rs := &packp.ReportStatus{UnpackStatus: statusOK}

	useSideband := req.Capabilities.Supports(capability.Sideband64k) ||
		req.Capabilities.Supports(capability.Sideband)
	wantReport := req.Capabilities.Supports(capability.ReportStatus)
	out := newResponder(w, useSideband, wantReport)

	if needPack {
		if err := unpack(repo.Storer, rd, limits); err != nil {
			rs.UnpackStatus = err.Error()
			fillStatuses(rs, updates, "unpack failed")
			return out.finish(rs)
		}
	}

	if err := hooks.PreReceive(ctx, repo, updates); err != nil {
		if rej, ok := errors.AsType[*RejectionError](err); ok {
			out.progress(rej.Sideband())
			fillStatuses(rs, updates, rej.Status())
		} else {
			out.progress([]string{"silo: pre-receive: " + err.Error()})
			fillStatuses(rs, updates, "pre-receive declined")
		}
		return out.finish(rs)
	}

	if err := applyUpdates(repo.Storer, updates, rs); err != nil {
		return out.finish(rs)
	}

	hooks.PostReceive(ctx, repo, updates)

	fillStatuses(rs, updates, statusOK)
	return out.finish(rs)
}

func applyUpdates(st storer.ReferenceStorer, updates []RefUpdate, rs *packp.ReportStatus) error {
	applied := make([]RefUpdate, 0, len(updates))
	rollback := func() {
		for _, u := range applied {
			if u.Old.IsZero() {
				_ = st.RemoveReference(u.Name)
			} else {
				_ = st.SetReference(plumbing.NewHashReference(u.Name, u.Old))
			}
		}
	}

	for _, u := range updates {
		var err error
		if u.IsDelete() {
			err = st.RemoveReference(u.Name)
		} else {
			err = st.SetReference(plumbing.NewHashReference(u.Name, u.New))
		}
		if err != nil {
			rollback()
			fillStatuses(rs, updates, "atomic transaction failed: "+err.Error())
			return err
		}
		applied = append(applied, u)
	}
	return nil
}

func fillStatuses(rs *packp.ReportStatus, updates []RefUpdate, status string) {
	if rs.CommandStatuses != nil {
		return
	}
	rs.CommandStatuses = make([]*packp.CommandStatus, 0, len(updates))
	for _, u := range updates {
		rs.CommandStatuses = append(rs.CommandStatuses, &packp.CommandStatus{
			ReferenceName: u.Name,
			Status:        status,
		})
	}
}

type responder struct {
	w        io.Writer
	mux      *sideband.Muxer
	sideband bool
	report   bool
}

func newResponder(w io.Writer, useSideband, report bool) *responder {
	r := &responder{w: w, sideband: useSideband, report: report}
	if useSideband {
		r.mux = sideband.NewMuxer(sideband.Sideband64k, w)
	}
	return r
}

func (r *responder) progress(lines []string) {
	if !r.sideband {
		return
	}
	for _, ln := range lines {
		_, _ = r.mux.WriteChannel(sideband.ProgressMessage, []byte(ln+"\n"))
	}
}

func (r *responder) finish(rs *packp.ReportStatus) error {
	if !r.report {
		if r.sideband {
			return pktline.WriteFlush(r.w)
		}
		return nil
	}
	if r.sideband {
		var buf bytes.Buffer
		if err := rs.Encode(&buf); err != nil {
			return err
		}
		if _, err := r.mux.Write(buf.Bytes()); err != nil {
			return err
		}
		return pktline.WriteFlush(r.w)
	}
	return rs.Encode(r.w)
}
