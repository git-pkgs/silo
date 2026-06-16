package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/store"
)

func TestHandler_Routes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gst, err := gitstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	repo, err := gst.Init("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	sha := seedCommit(t, repo)

	srv := httptest.NewServer(Handler(st, gst, "http://silo.test", "SHA256:forge"))
	t.Cleanup(srv.Close)

	cases := []struct {
		path     string
		want     int
		contains string
	}{
		{"/", http.StatusOK, "alice/demo"},
		{"/alice/demo", http.StatusOK, "refs/heads/main"},
		{"/alice/demo/", http.StatusOK, "refs/heads/main"},
		{"/alice/demo/tree/refs/heads/main", http.StatusOK, "README.md"},
		{"/alice/demo/tree/HEAD", http.StatusOK, "README.md"},
		{"/alice/demo/tree/" + sha, http.StatusOK, "README.md"},
		{"/alice/demo/blob/refs/heads/main/README.md", http.StatusOK, "<h1>hello</h1>"},
		{"/alice/demo/raw/refs/heads/main/README.md", http.StatusOK, "# hello"},
		{"/alice/demo/tree/refs/heads/main/nope", http.StatusNotFound, ""},
		{"/alice/demo/blob/refs/heads/main/nope", http.StatusNotFound, ""},
		{"/alice/demo/tree/refs/heads/nope", http.StatusNotFound, ""},
		{"/alice/demo/log/refs/heads/main", http.StatusOK, "seed"},
		{"/alice/demo/log/HEAD", http.StatusOK, "seed"},
		{"/alice/demo/commit/" + sha, http.StatusOK, sha},
		{"/alice/demo/rsl", http.StatusOK, "Reference state log"},
		{"/alice/demo/policy", http.StatusOK, "Policy"},
		{"/alice/demo/verify", http.StatusOK, "refs/heads/main"},
		{"/alice/demo/compare/main...main", http.StatusOK, "0 commit"},
		{"/alice/demo/compare/main", http.StatusBadRequest, ""},
		{"/alice/demo/compare/main...nope", http.StatusNotFound, ""},
		{"/alice/demo/branches", http.StatusOK, "main"},
		{"/alice/demo/tags", http.StatusOK, "Tags"},
		{"/alice/demo/policy/history", http.StatusOK, "Policy history"},
		{"/alice/demo/rsl/refs/heads/main", http.StatusOK, "Reference state log"},
		{"/alice/demo/attestations", http.StatusOK, "Attestations"},
		{"/alice/demo/hooks", http.StatusOK, "Hooks"},
		{"/alice/demo/principal/nobody", http.StatusNotFound, ""},
		{"/activity", http.StatusOK, "Activity"},
		{"/nobody/nothing", http.StatusNotFound, ""},
		{"/alice/demo/log/refs/heads/absent", http.StatusNotFound, ""},
		{"/alice/demo/commit/0000000000000000000000000000000000000000", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := srv.Client().Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("GET %s: status = %d, want %d; body:\n%s", tc.path, resp.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusOK && !strings.Contains(tc.path, "/raw/") {
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("Content-Type = %q, want text/html", ct)
				}
			}
			if tc.contains != "" && !strings.Contains(string(body), tc.contains) {
				t.Errorf("body does not contain %q:\n%s", tc.contains, body)
			}
		})
	}
}

func TestStaticHandler(t *testing.T) {
	srv := httptest.NewServer(StaticHandler())
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Get(srv.URL + "/style.css")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /style.css: status = %d", resp.StatusCode)
	}
}

func TestLoadTemplates(t *testing.T) {
	tm, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index", "repo", "log", "commit", "rsl", "policy", "tree", "blob"} {
		if tm[name] == nil {
			t.Errorf("template %q not loaded", name)
		}
	}
}

func TestRenderMarkup(t *testing.T) {
	out := renderMarkup("README.md", []byte("# hi\n\n<script>alert(1)</script>\n"))
	if !strings.Contains(string(out), "<h1>hi</h1>") {
		t.Errorf("markdown heading not rendered: %s", out)
	}
	if strings.Contains(string(out), "<script") {
		t.Errorf("script tag survived sanitizer: %s", out)
	}
	if renderMarkup("file.go", []byte("package x")) != "" {
		t.Error("unsupported format should return empty")
	}
}

func TestSplitRefPath(t *testing.T) {
	dir := t.TempDir()
	gst, _ := gitstore.Open(dir)
	repo, _ := gst.Init("a", "b")
	sha := seedCommit(t, repo)
	cases := []struct{ in, ref, path string }{
		{"refs/heads/main", "refs/heads/main", ""},
		{"refs/heads/main/x/y.go", "refs/heads/main", "x/y.go"},
		{"HEAD/README.md", "HEAD", "README.md"},
		{sha + "/a/b", sha, "a/b"},
	}
	for _, tc := range cases {
		ref, _, p, ok := splitRefPath(repo, tc.in)
		if !ok || ref != tc.ref || p != tc.path {
			t.Errorf("splitRefPath(%q) = %q, %q, %v; want %q, %q", tc.in, ref, p, ok, tc.ref, tc.path)
		}
	}
	if _, _, _, ok := splitRefPath(repo, "refs/heads/nope/x"); ok {
		t.Error("unknown ref should not resolve")
	}
}

func TestHelpers(t *testing.T) {
	if firstLine("a\nb\nc") != "a" || firstLine("abc") != "abc" {
		t.Error("firstLine wrong")
	}
	if ago(time.Now().Add(-30*time.Second)) != "just now" {
		t.Error("ago(now) should be 'just now'")
	}
	if got := ago(time.Now().Add(-2 * time.Hour)); !strings.HasSuffix(got, "h ago") {
		t.Errorf("ago(2h) = %q", got)
	}
}

func seedCommit(t *testing.T, repo *git.Repository) string {
	t.Helper()
	st := repo.Storer
	bh := writeEnc(t, st, &plumbing.MemoryObject{}, func(o plumbing.EncodedObject) {
		o.SetType(plumbing.BlobObject)
		w, _ := o.Writer()
		_, _ = w.Write([]byte("# hello\n"))
		_ = w.Close()
	})
	tree := &object.Tree{Entries: []object.TreeEntry{{Name: "README.md", Mode: 0o100644, Hash: bh}}}
	th := writeEnc(t, st, st.NewEncodedObject(), func(o plumbing.EncodedObject) { _ = tree.Encode(o) })
	sig := object.Signature{Name: "alice", Email: "a@x", When: time.Now()}
	c := &object.Commit{TreeHash: th, Author: sig, Committer: sig, Message: "seed\n"}
	ch := writeEnc(t, st, st.NewEncodedObject(), func(o plumbing.EncodedObject) { _ = c.Encode(o) })
	if err := st.SetReference(plumbing.NewHashReference("refs/heads/main", ch)); err != nil {
		t.Fatal(err)
	}
	return ch.String()
}

func writeEnc(t *testing.T, st storer.EncodedObjectStorer, o plumbing.EncodedObject, fill func(plumbing.EncodedObject)) plumbing.Hash {
	t.Helper()
	fill(o)
	h, err := st.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
