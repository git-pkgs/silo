package web

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
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
		{"/alice/demo/rsl", http.StatusOK, "refs/heads/main"},
		{"/alice/demo/policy", http.StatusOK, "Policy"},
		{"/alice/demo/verify", http.StatusOK, "refs/heads/main"},
		{"/alice/demo/compare/main...main", http.StatusOK, "0 commit"},
		{"/alice/demo/compare/main", http.StatusBadRequest, ""},
		{"/alice/demo/compare/main...nope", http.StatusNotFound, ""},
		{"/alice/demo/branches", http.StatusOK, "main"},
		{"/alice/demo/tags", http.StatusOK, "v1"},
		{"/alice/demo/policy/history", http.StatusOK, "Initialize policy"},
		{"/alice/demo/rsl/refs/heads/main", http.StatusOK, "annotation"},
		{"/alice/demo/attestations", http.StatusOK, "Attestations"},
		{"/alice/demo/hooks", http.StatusOK, "Hooks"},
		{"/alice/demo/principal/nobody", http.StatusNotFound, ""},
		{"/alice/demo/principal/silo", http.StatusOK, "SHA256:forge"},
		{"/alice/demo/compare/refs/heads/dev...refs/heads/main", http.StatusOK, "1 commit"},
		{"/alice/demo/log/refs/heads/main?after=" + sha, http.StatusOK, "seed"},
		{"/activity", http.StatusOK, "Activity"},
		{"/alice/demo/blame/refs/heads/main/README.md", http.StatusOK, "hello"},
		{"/alice/demo/history/refs/heads/main/README.md", http.StatusOK, "seed"},
		{"/alice/demo/history/refs/heads/main", http.StatusNotFound, ""},
		{"/alice/demo/contributors", http.StatusOK, "alice"},
		{"/alice/demo/search/refs/heads/main?q=read", http.StatusOK, "README.md"},
		{"/alice/demo/search/refs/heads/main?q=nope", http.StatusOK, "no matches"},
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

func TestArchive(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	t.Cleanup(func() { _ = st.Close() })
	gst, _ := gitstore.Open(dir)
	_, _ = st.CreateRepo("a", "r")
	repo, _ := gst.Init("a", "r")
	seedCommit(t, repo)
	srv := httptest.NewServer(Handler(st, gst, "http://x", ""))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/a/r/archive/refs/heads/main.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("status=%d ct=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "r-main/README.md" {
		t.Fatalf("first entry = %v, %v", hdr, err)
	}
	b, _ := io.ReadAll(tr)
	if !strings.HasPrefix(string(b), "# hello") {
		t.Errorf("content = %q", b)
	}
}

func TestJSONFormat(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	t.Cleanup(func() { _ = st.Close() })
	gst, _ := gitstore.Open(dir)
	_, _ = st.CreateRepo("a", "r")
	repo, _ := gst.Init("a", "r")
	seedCommit(t, repo)
	srv := httptest.NewServer(Handler(st, gst, "http://x", ""))
	t.Cleanup(srv.Close)

	for _, p := range []string{"/?format=json", "/a/r/rsl?format=json", "/a/r/tree/HEAD?format=json"} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q", p, ct)
		}
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Errorf("%s: decode: %v", p, err)
		}
		_ = resp.Body.Close()
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
	sig := object.Signature{Name: "alice", Email: "a@x", When: time.Now()}

	bh := writeBlob(t, st, "# hello\n")
	t1 := writeTree(t, st, []object.TreeEntry{{Name: "README.md", Mode: 0o100644, Hash: bh}})
	c1 := writeCommit(t, st, &object.Commit{TreeHash: t1, Author: sig, Committer: sig, Message: "seed\n"})

	bh2 := writeBlob(t, st, "# hello\n\nmore\n")
	t2 := writeTree(t, st, []object.TreeEntry{{Name: "README.md", Mode: 0o100644, Hash: bh2}})
	c2 := writeCommit(t, st, &object.Commit{TreeHash: t2, ParentHashes: []plumbing.Hash{c1}, Author: sig, Committer: sig, Message: "seed\n"})
	setRef(t, st, "refs/heads/main", c2)
	setRef(t, st, "refs/heads/dev", c1)

	tagH := writeEnc(t, st, st.NewEncodedObject(), func(o plumbing.EncodedObject) {
		_ = (&object.Tag{Name: "v1", Tagger: sig, Message: "v1", Target: c2, TargetType: plumbing.CommitObject}).Encode(o)
	})
	setRef(t, st, "refs/tags/v1", tagH)

	pj := writeBlob(t, st, `{"fake":"policy"}`)
	mt := writeTree(t, st, []object.TreeEntry{{Name: "targets.json", Mode: 0o100644, Hash: pj}})
	pt := writeTree(t, st, []object.TreeEntry{{Name: "metadata", Mode: 0o40000, Hash: mt}})
	pc := writeCommit(t, st, &object.Commit{TreeHash: pt, Author: sig, Committer: sig, Message: "Initialize policy\n"})
	setRef(t, st, "refs/gittuf/policy", pc)

	r1 := writeCommit(t, st, &object.Commit{TreeHash: t1, Committer: sig,
		Message: "RSL Reference Entry\n\nref: refs/heads/main\ntargetID: " + c2.String() + "\nnumber: 1\n"})
	r2 := writeCommit(t, st, &object.Commit{TreeHash: t1, ParentHashes: []plumbing.Hash{r1}, Committer: sig,
		Message: "RSL Annotation Entry\n\nentryID: " + r1.String() + "\nnumber: 2\n"})
	setRef(t, st, "refs/gittuf/reference-state-log", r2)

	return c2.String()
}

func writeBlob(t testing.TB, st storer.EncodedObjectStorer, s string) plumbing.Hash {
	return writeEnc(t, st, &plumbing.MemoryObject{}, func(o plumbing.EncodedObject) {
		o.SetType(plumbing.BlobObject)
		w, _ := o.Writer()
		_, _ = w.Write([]byte(s))
		_ = w.Close()
	})
}

func writeTree(t testing.TB, st storer.EncodedObjectStorer, e []object.TreeEntry) plumbing.Hash {
	return writeEnc(t, st, st.NewEncodedObject(), func(o plumbing.EncodedObject) { _ = (&object.Tree{Entries: e}).Encode(o) })
}

func writeCommit(t testing.TB, st storer.EncodedObjectStorer, c *object.Commit) plumbing.Hash {
	return writeEnc(t, st, st.NewEncodedObject(), func(o plumbing.EncodedObject) { _ = c.Encode(o) })
}

func setRef(t testing.TB, st storer.ReferenceStorer, name string, h plumbing.Hash) {
	t.Helper()
	if err := st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), h)); err != nil {
		t.Fatal(err)
	}
}

func writeEnc(t testing.TB, st storer.EncodedObjectStorer, o plumbing.EncodedObject, fill func(plumbing.EncodedObject)) plumbing.Hash {
	t.Helper()
	fill(o)
	h, err := st.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
