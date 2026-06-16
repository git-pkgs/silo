package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	gssh "github.com/gliderlabs/ssh"
	"github.com/go-git/go-git/v5/plumbing/transport"
	xssh "golang.org/x/crypto/ssh"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/receive"
	"github.com/git-pkgs/silo/internal/store"
)

func TestLoader(t *testing.T) {
	gst, err := gitstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gst.Init("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	l := &loader{gst: gst}

	ep, _ := transport.NewEndpoint("/alice/demo.git")
	if _, err := l.Load(ep); err != nil {
		t.Errorf("Load existing: %v", err)
	}
	ep2, _ := transport.NewEndpoint("/alice/missing.git")
	if _, err := l.Load(ep2); err == nil {
		t.Error("Load missing should fail")
	}
	ep3, _ := transport.NewEndpoint("/one")
	if _, err := l.Load(ep3); err == nil {
		t.Error("Load malformed should fail")
	}
}

type fakeCtx struct {
	gssh.Context
	vals map[any]any
}

func (c *fakeCtx) SetValue(k, v any)   { c.vals[k] = v }
func (c *fakeCtx) Value(k any) any     { return c.vals[k] }
func (c *fakeCtx) SessionID() string   { return "" }
func (c *fakeCtx) ClientVersion() string { return "" }
func (c *fakeCtx) ServerVersion() string { return "" }
func (c *fakeCtx) User() string        { return "" }

func TestPublicKeyHandler(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := xssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	fp := xssh.FingerprintSHA256(pk)

	u, err := st.CreateUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSSHKey(u.ID, fp, string(xssh.MarshalAuthorizedKey(pk))); err != nil {
		t.Fatal(err)
	}

	h := publicKeyHandler(st)
	ctx := &fakeCtx{vals: map[any]any{}}
	if !h(ctx, pk) {
		t.Fatal("handler rejected registered key")
	}
	p, ok := ctx.vals[ctxUserKey].(receive.Pusher)
	if !ok || p.User != "alice" || p.KeyFingerprint != fp {
		t.Errorf("ctx pusher = %+v", p)
	}

	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	pk2, _ := xssh.NewPublicKey(pub2)
	if h(&fakeCtx{vals: map[any]any{}}, pk2) {
		t.Error("handler accepted unregistered key")
	}
}

func TestParseExec(t *testing.T) {
	tests := []struct {
		in                   string
		cmd, owner, name     string
		ok                   bool
	}{
		{"git-upload-pack 'alice/demo.git'", "git-upload-pack", "alice", "demo", true},
		{"git-receive-pack '/alice/demo.git'", "git-receive-pack", "alice", "demo", true},
		{`git-upload-pack "alice/demo"`, "git-upload-pack", "alice", "demo", true},
		{"git-receive-pack alice/demo.git", "git-receive-pack", "alice", "demo", true},
		{"git-upload-pack '/just-one'", "", "", "", false},
		{"git-upload-pack 'a/b/c'", "", "", "", false},
		{"rm -rf /", "", "", "", false},
		{"git-upload-pack", "", "", "", false},
		{"", "", "", "", false},
		{"git-cat-file 'a/b'", "", "", "", false},
	}
	for _, tc := range tests {
		cmd, o, n, err := parseExec(tc.in)
		if tc.ok {
			if err != nil || cmd != tc.cmd || o != tc.owner || n != tc.name {
				t.Errorf("parseExec(%q) = %q,%q,%q,%v", tc.in, cmd, o, n, err)
			}
		} else if err == nil {
			t.Errorf("parseExec(%q) accepted: %q,%q,%q", tc.in, cmd, o, n)
		}
	}
}
