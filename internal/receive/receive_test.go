package receive

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
)

const refMain = "refs/heads/main"

func TestAdapt_PreReceiveRejection(t *testing.T) {
	rej := &RejectionError{
		Ref:        refMain,
		Rule:       "protect-main",
		Threshold:  2,
		Principals: []string{"alice", "bob"},
		Pusher:     "carol",
		PusherKey:  "SHA256:x",
		PolicyURL:  "https://silo/x/y/policy#protect-main",
	}
	h := adapt(nil, fnHooks{pre: func() error { return rej }})

	var progress bytes.Buffer
	err := h.PreReceive(context.Background(), nil,
		[]*packp.Command{{Name: refMain}}, &progress)

	if err == nil {
		t.Fatal("PreReceive returned nil for rejection")
	}
	if !strings.Contains(err.Error(), StatusPolicy) {
		t.Errorf("err = %q, want to contain %q", err.Error(), StatusPolicy)
	}
	out := progress.String()
	for _, want := range []string{
		"silo: rejected refs/heads/main",
		"rule 'protect-main' requires 2 of: alice, bob",
		"you pushed as: carol",
		"policy: https://silo/x/y/policy#protect-main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("progress missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestAdapt_PreReceivePlainError(t *testing.T) {
	h := adapt(nil, fnHooks{pre: func() error { return errors.New("nope") }})
	var progress bytes.Buffer
	err := h.PreReceive(context.Background(), nil, nil, &progress)
	if err == nil || !strings.Contains(err.Error(), "pre-receive declined") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(progress.String(), "nope") {
		t.Errorf("progress = %q", progress.String())
	}
}

func TestAdapt_PreReceiveOK(t *testing.T) {
	var postCalled bool
	h := adapt(nil, fnHooks{
		pre:  func() error { return nil },
		post: func() { postCalled = true },
	})
	if err := h.PreReceive(context.Background(), nil, nil, &bytes.Buffer{}); err != nil {
		t.Errorf("PreReceive = %v", err)
	}
	h.PostReceive(context.Background(), nil, nil)
	if !postCalled {
		t.Error("PostReceive not called")
	}
}

func TestToUpdates(t *testing.T) {
	h := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	u := toUpdates([]*packp.Command{
		{Name: "refs/heads/a", Old: plumbing.ZeroHash, New: h},
		{Name: "refs/heads/b", Old: h, New: plumbing.ZeroHash},
	})
	if len(u) != 2 || u[0].Name != "refs/heads/a" || !u[1].IsDelete() {
		t.Errorf("toUpdates = %+v", u)
	}
}

func TestPusherContext(t *testing.T) {
	if _, ok := PusherFrom(context.Background()); ok {
		t.Error("PusherFrom on empty context returned ok")
	}
	p := Pusher{User: "alice", KeyFingerprint: "SHA256:abc"}
	got, ok := PusherFrom(WithPusher(context.Background(), p))
	if !ok || got != p {
		t.Errorf("PusherFrom = %+v, %v", got, ok)
	}
}

func TestRepoPathContext(t *testing.T) {
	if _, ok := RepoPathFrom(context.Background()); ok {
		t.Error("RepoPathFrom on empty context returned ok")
	}
	got, ok := RepoPathFrom(WithRepoPath(context.Background(), "/x/y.git"))
	if !ok || got != "/x/y.git" {
		t.Errorf("RepoPathFrom = %q, %v", got, ok)
	}
}

type fnHooks struct {
	pre  func() error
	post func()
}

func (h fnHooks) PreReceive(context.Context, *git.Repository, []RefUpdate) error {
	if h.pre != nil {
		return h.pre()
	}
	return nil
}

func (h fnHooks) PostReceive(context.Context, *git.Repository, []RefUpdate) {
	if h.post != nil {
		h.post()
	}
}
