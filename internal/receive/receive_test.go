package receive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

const refMain = "refs/heads/main"

type fnHooks struct {
	pre  func(context.Context, *git.Repository, []RefUpdate) error
	post func(context.Context, *git.Repository, []RefUpdate)
}

func (h fnHooks) PreReceive(ctx context.Context, r *git.Repository, u []RefUpdate) error {
	if h.pre != nil {
		return h.pre(ctx, r, u)
	}
	return nil
}

func (h fnHooks) PostReceive(ctx context.Context, r *git.Repository, u []RefUpdate) {
	if h.post != nil {
		h.post(ctx, r, u)
	}
}

func TestAdvertise(t *testing.T) {
	src, h := newSourceCommit(t)
	var buf bytes.Buffer
	if err := Advertise(src, &buf); err != nil {
		t.Fatalf("advertise: %v", err)
	}
	out := buf.String()
	for _, want := range []string{h.String(), refMain, "report-status", "side-band-64k", "delete-refs", "agent=silo/"} {
		if !strings.Contains(out, want) {
			t.Errorf("advertisement missing %q\n%s", want, out)
		}
	}
}

func TestAdvertise_EmptyRepo(t *testing.T) {
	repo := newBareRepo(t)
	var buf bytes.Buffer
	if err := Advertise(repo, &buf); err != nil {
		t.Fatalf("advertise: %v", err)
	}
	if !strings.Contains(buf.String(), plumbing.ZeroHash.String()) {
		t.Errorf("empty advertisement should contain zero hash, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "report-status") {
		t.Errorf("empty advertisement missing capabilities")
	}
}

func TestReceive_SingleRef(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)

	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus)

	var postCalled bool
	hooks := fnHooks{post: func(_ context.Context, _ *git.Repository, u []RefUpdate) {
		postCalled = true
		if len(u) != 1 || u[0].Name != refMain || u[0].New != head {
			t.Errorf("post-receive updates = %+v", u)
		}
	}}

	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, hooks, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}

	ref, err := target.Reference(refMain, false)
	if err != nil {
		t.Fatalf("ref not created: %v", err)
	}
	if ref.Hash() != head {
		t.Errorf("ref = %s, want %s", ref.Hash(), head)
	}
	if _, err := target.CommitObject(head); err != nil {
		t.Errorf("commit object missing from target: %v", err)
	}
	if !postCalled {
		t.Error("PostReceive not called")
	}

	rs := decodeReport(t, out.Bytes())
	if rs.UnpackStatus != "ok" {
		t.Errorf("unpack status = %q", rs.UnpackStatus)
	}
	if len(rs.CommandStatuses) != 1 || rs.CommandStatuses[0].Status != "ok" {
		t.Errorf("command statuses = %+v", rs.CommandStatuses)
	}
}

func TestReceive_PreReceiveReject(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)

	rej := &RejectionError{
		Ref:        refMain,
		Rule:       "protect-main",
		Threshold:  2,
		Principals: []string{"alice", "bob", "carol"},
		Pusher:     "andrew",
		PusherKey:  "SHA256:tL3x",
		PolicyURL:  "https://silo.example.com/andrew/demo/policy#protect-main",
	}
	var postCalled bool
	hooks := fnHooks{
		pre:  func(context.Context, *git.Repository, []RefUpdate) error { return rej },
		post: func(context.Context, *git.Repository, []RefUpdate) { postCalled = true },
	}

	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus, capability.Sideband64k)

	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, hooks, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}

	if _, err := target.Reference(refMain, false); err == nil {
		t.Error("ref was created despite rejection")
	}
	if postCalled {
		t.Error("PostReceive called after rejection")
	}

	progress, report := demuxSideband(t, out.Bytes())
	for _, want := range []string{
		"silo: rejected refs/heads/main",
		"rule 'protect-main' requires 2 of: alice, bob, carol",
		"you pushed as: andrew (SHA256:tL3x) — not in principal set",
		"approvals on record: 0/2",
		"policy: https://silo.example.com/andrew/demo/policy#protect-main",
	} {
		if !strings.Contains(progress, want) {
			t.Errorf("sideband missing %q\ngot:\n%s", want, progress)
		}
	}

	rs := decodeReport(t, report)
	if rs.UnpackStatus != "ok" {
		t.Errorf("unpack status = %q", rs.UnpackStatus)
	}
	if len(rs.CommandStatuses) != 1 || rs.CommandStatuses[0].Status != "policy" {
		t.Errorf("command status = %+v, want ng policy", rs.CommandStatuses)
	}
}

func TestReceive_PreReceivePlainError(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)
	hooks := fnHooks{pre: func(context.Context, *git.Repository, []RefUpdate) error {
		return errors.New("nope")
	}}
	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus)
	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, hooks, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	rs := decodeReport(t, out.Bytes())
	if rs.CommandStatuses[0].Status != "pre-receive declined" {
		t.Errorf("status = %q", rs.CommandStatuses[0].Status)
	}
}

func TestReceive_Delete(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)
	if err := target.Storer.SetReference(plumbing.NewHashReference(refMain, head)); err != nil {
		t.Fatalf("seed ref: %v", err)
	}

	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: head, New: plumbing.ZeroHash},
	}, capability.ReportStatus)

	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, nil, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	if _, err := target.Reference(refMain, false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Errorf("ref still present after delete: %v", err)
	}
	rs := decodeReport(t, out.Bytes())
	if rs.CommandStatuses[0].Status != "ok" {
		t.Errorf("delete status = %q", rs.CommandStatuses[0].Status)
	}
}

func TestReceive_PackTooLarge(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)

	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus)

	var out bytes.Buffer
	err := ReceivePack(context.Background(), target, in, &out, nil, Limits{MaxPackBytes: 1, MaxObjects: 100})
	if err != nil {
		t.Fatalf("ReceivePack returned error (should report via status): %v", err)
	}
	rs := decodeReport(t, out.Bytes())
	if !strings.Contains(rs.UnpackStatus, "unpack-limit") {
		t.Errorf("unpack status = %q, want unpack-limit", rs.UnpackStatus)
	}
	if _, err := target.Reference(refMain, false); err == nil {
		t.Error("ref created despite unpack failure")
	}
}

func TestReceive_TooManyObjects(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)

	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus)

	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, nil, Limits{MaxPackBytes: 1 << 20, MaxObjects: 1}); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	rs := decodeReport(t, out.Bytes())
	if !strings.Contains(rs.UnpackStatus, "MaxObjects") {
		t.Errorf("unpack status = %q, want MaxObjects", rs.UnpackStatus)
	}
}

func TestReceive_Atomic(t *testing.T) {
	_, head := newSourceCommit(t)
	const refA, refB = "refs/heads/a", "refs/heads/b"

	st := &refStore{refs: map[plumbing.ReferenceName]plumbing.Hash{}, failOn: refB}
	updates := []RefUpdate{
		{Name: refA, Old: plumbing.ZeroHash, New: head},
		{Name: refB, Old: plumbing.ZeroHash, New: head},
	}
	rs := &packp.ReportStatus{}
	rs.UnpackStatus = "ok"

	if err := applyUpdates(st, updates, rs); err == nil {
		t.Fatal("applyUpdates did not return error")
	}
	if _, ok := st.refs[refA]; ok {
		t.Errorf("refA not rolled back: %v", st.refs)
	}
	for _, cs := range rs.CommandStatuses {
		if !strings.Contains(cs.Status, "atomic") {
			t.Errorf("status for %s = %q, want atomic failure", cs.ReferenceName, cs.Status)
		}
	}
}

func TestReceive_AtomicRollbackToOld(t *testing.T) {
	old := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	neu := plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	const refA, refB = "refs/heads/a", "refs/heads/b"

	st := &refStore{refs: map[plumbing.ReferenceName]plumbing.Hash{refA: old}, failOn: refB}
	updates := []RefUpdate{
		{Name: refA, Old: old, New: neu},
		{Name: refB, Old: plumbing.ZeroHash, New: neu},
	}
	rs := &packp.ReportStatus{}

	if err := applyUpdates(st, updates, rs); err == nil {
		t.Fatal("applyUpdates did not return error")
	}
	if got := st.refs[refA]; got != old {
		t.Errorf("refA = %s, want rolled back to %s", got, old)
	}
}

func TestReceive_NoReportStatus(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)
	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	})
	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, nil, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("output without report-status should be empty, got %d bytes", out.Len())
	}
	if _, err := target.Reference(refMain, false); err != nil {
		t.Errorf("ref not created: %v", err)
	}
}

func TestReceive_SidebandSuccess(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)
	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus, capability.Sideband64k)
	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, nil, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	_, report := demuxSideband(t, out.Bytes())
	rs := decodeReport(t, report)
	if rs.CommandStatuses[0].Status != "ok" {
		t.Errorf("status = %q", rs.CommandStatuses[0].Status)
	}
}

func TestReceive_SidebandNoReport(t *testing.T) {
	src, head := newSourceCommit(t)
	target := newBareRepo(t)
	in := encodePush(t, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.Sideband64k)
	var out bytes.Buffer
	if err := ReceivePack(context.Background(), target, in, &out, nil, DefaultLimits()); err != nil {
		t.Fatalf("ReceivePack: %v", err)
	}
	if out.String() != "0000" {
		t.Errorf("expected only flush-pkt, got %q", out.String())
	}
}

func TestPusherContext(t *testing.T) {
	if _, ok := PusherFrom(context.Background()); ok {
		t.Error("PusherFrom on empty context returned ok")
	}
	p := Pusher{User: "alice", KeyFingerprint: "SHA256:abc"}
	ctx := WithPusher(context.Background(), p)
	got, ok := PusherFrom(ctx)
	if !ok || got != p {
		t.Errorf("PusherFrom = %+v, %v", got, ok)
	}
}

func TestRepoPathContext(t *testing.T) {
	if _, ok := RepoPathFrom(context.Background()); ok {
		t.Error("RepoPathFrom on empty context returned ok")
	}
	ctx := WithRepoPath(context.Background(), "/x/y.git")
	got, ok := RepoPathFrom(ctx)
	if !ok || got != "/x/y.git" {
		t.Errorf("RepoPathFrom = %q, %v", got, ok)
	}
}

// demuxSideband splits a sideband-wrapped response into progress text and the
// band-1 report bytes.
func demuxSideband(t *testing.T, b []byte) (progress string, report []byte) {
	t.Helper()
	var prog bytes.Buffer
	d := sideband.NewDemuxer(sideband.Sideband64k, bytes.NewReader(b))
	d.Progress = &prog
	rep, err := io.ReadAll(d)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	return prog.String(), rep
}

// refStore is a minimal storer.ReferenceStorer for testing applyUpdates
// rollback. SetReference fails when the ref name matches failOn.
type refStore struct {
	refs   map[plumbing.ReferenceName]plumbing.Hash
	failOn plumbing.ReferenceName
}

func (s *refStore) SetReference(r *plumbing.Reference) error {
	if r.Name() == s.failOn {
		return fmt.Errorf("simulated failure on %s", r.Name())
	}
	s.refs[r.Name()] = r.Hash()
	return nil
}

func (s *refStore) RemoveReference(n plumbing.ReferenceName) error {
	delete(s.refs, n)
	return nil
}

func (s *refStore) CheckAndSetReference(n, _ *plumbing.Reference) error { return s.SetReference(n) }
func (s *refStore) Reference(n plumbing.ReferenceName) (*plumbing.Reference, error) {
	if h, ok := s.refs[n]; ok {
		return plumbing.NewHashReference(n, h), nil
	}
	return nil, plumbing.ErrReferenceNotFound
}
func (s *refStore) IterReferences() (storer.ReferenceIter, error) { return nil, nil }
func (s *refStore) CountLooseRefs() (int, error)                  { return len(s.refs), nil }
func (s *refStore) PackRefs() error                               { return nil }
