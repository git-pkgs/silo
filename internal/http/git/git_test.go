package git

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/git-pkgs/silo/internal/gitstore"
)

func newServer(t *testing.T) (*httptest.Server, *gitstore.Store) {
	t.Helper()
	st, err := gitstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(st))
	t.Cleanup(srv.Close)
	return srv, st
}

func seed(t *testing.T, st *gitstore.Store, owner, name string) {
	t.Helper()
	p, err := st.Path(owner, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
	repo, err := gogit.PlainInitWithOptions(p, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: "refs/heads/main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()}
	if _, err := wt.Commit("seed", &gogit.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSplitRepoPath(t *testing.T) {
	tests := []struct {
		in          string
		owner, name string
		ok          bool
	}{
		{"/alice/demo.git", "alice", "demo", true},
		{"alice/demo.git", "alice", "demo", true},
		{"/alice/demo", "alice", "demo", true},
		{"/alice", "", "", false},
		{"/", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		o, n, ok := splitRepoPath(tc.in)
		if ok != tc.ok || o != tc.owner || n != tc.name {
			t.Errorf("splitRepoPath(%q) = %q,%q,%v", tc.in, o, n, ok)
		}
	}
}

func TestLoader(t *testing.T) {
	_, st := newServer(t)
	seed(t, st, "alice", "demo")
	l := &loader{st: st}

	ep, _ := transport.NewEndpoint("/alice/demo.git")
	if _, err := l.Load(ep); err != nil {
		t.Errorf("Load existing: %v", err)
	}
	ep2, _ := transport.NewEndpoint("/alice/missing.git")
	if _, err := l.Load(ep2); err == nil {
		t.Error("Load missing should fail")
	}
	ep3, _ := transport.NewEndpoint("/nopath")
	if _, err := l.Load(ep3); err == nil {
		t.Error("Load malformed should fail")
	}
}

func TestInfoRefs(t *testing.T) {
	srv, st := newServer(t)
	seed(t, st, "alice", "demo")

	resp, err := http.Get(srv.URL + "/alice/demo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "git-upload-pack-advertisement") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestInfoRefs_WrongService(t *testing.T) {
	srv, st := newServer(t)
	seed(t, st, "alice", "demo")

	for _, q := range []string{"", "?service=git-receive-pack"} {
		resp, err := http.Get(srv.URL + "/alice/demo.git/info/refs" + q)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status for %q = %d, want 404", q, resp.StatusCode)
		}
	}
}

func TestInfoRefs_NotFound(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/x/y.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestReceivePackRefused(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Post(srv.URL+"/x/y.git/git-receive-pack", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUploadPack_NotFound(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Post(srv.URL+"/x/y.git/git-upload-pack", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRequestBody_Gzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("hello"))
	_ = gz.Close()

	r := httptest.NewRequest(http.MethodPost, "/", &buf)
	r.Header.Set("Content-Encoding", "gzip")
	body, err := requestBody(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	got, _ := io.ReadAll(body)
	if string(got) != "hello" {
		t.Errorf("body = %q", got)
	}

	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-gzip"))
	r2.Header.Set("Content-Encoding", "gzip")
	if _, err := requestBody(r2); err == nil {
		t.Error("requestBody accepted bad gzip")
	}
}

func TestUploadPack_BadBody(t *testing.T) {
	srv, st := newServer(t)
	seed(t, st, "alice", "demo")
	resp, err := http.Post(srv.URL+"/alice/demo.git/git-upload-pack",
		"application/x-git-upload-pack-request", strings.NewReader("garbage"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestClone(t *testing.T) {
	srv, st := newServer(t)
	seed(t, st, "alice", "demo")

	_, err := gogit.Clone(memory.NewStorage(), nil, &gogit.CloneOptions{
		URL: srv.URL + "/alice/demo.git",
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
}

func TestDecodeHaves(t *testing.T) {
	in := "0032have aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"0000" +
		"0032have bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"0009done\n"
	var h packp.UploadHaves
	if err := decodeHaves(strings.NewReader(in), &h); err != nil {
		t.Fatalf("decodeHaves: %v", err)
	}
	if len(h.Haves) != 2 {
		t.Errorf("haves = %v", h.Haves)
	}

	if err := decodeHaves(strings.NewReader("0009junk\n"), &packp.UploadHaves{}); err == nil {
		t.Error("decodeHaves accepted junk line")
	}
}
