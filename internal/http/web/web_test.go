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
		{"/alice/demo/log/refs/heads/main", http.StatusOK, "seed"},
		{"/alice/demo/log/HEAD", http.StatusOK, "seed"},
		{"/alice/demo/commit/" + sha, http.StatusOK, sha},
		{"/alice/demo/rsl", http.StatusOK, "Reference state log"},
		{"/alice/demo/policy", http.StatusOK, "Policy"},
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
			if tc.want == http.StatusOK {
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
	for _, name := range []string{"index", "repo", "log", "commit", "rsl", "policy"} {
		if tm[name] == nil {
			t.Errorf("template %q not loaded", name)
		}
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
	tenc := st.NewEncodedObject()
	if err := (&object.Tree{}).Encode(tenc); err != nil {
		t.Fatal(err)
	}
	th, err := st.SetEncodedObject(tenc)
	if err != nil {
		t.Fatal(err)
	}
	sig := object.Signature{Name: "alice", Email: "a@x", When: time.Now()}
	cenc := st.NewEncodedObject()
	c := &object.Commit{TreeHash: th, Author: sig, Committer: sig, Message: "seed\n"}
	if err := c.Encode(cenc); err != nil {
		t.Fatal(err)
	}
	ch, err := st.SetEncodedObject(cenc)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReference(plumbing.NewHashReference("refs/heads/main", ch)); err != nil {
		t.Fatal(err)
	}
	return ch.String()
}
