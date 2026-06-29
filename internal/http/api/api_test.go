package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/http/api"
	"github.com/git-pkgs/silo/internal/pkgs"
	"github.com/git-pkgs/silo/internal/store"
)

const goMod1 = `module example.com/demo

go 1.26

require (
	github.com/spf13/cobra v1.8.0
	github.com/stretchr/testify v1.9.0
)
`

const goMod2 = `module example.com/demo

go 1.26

require (
	github.com/spf13/cobra v1.9.0
	github.com/stretchr/testify v1.9.0
)
`

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	srv        *httptest.Server
	st         *store.Store
	repoID     int64
	sha1, sha2 string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gst, err := gitstore.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.CreateRepo("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gst.Init("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	barePath, _ := gst.Path("alice", "demo")

	work := filepath.Join(t.TempDir(), "wt")
	runGit(t, filepath.Dir(work), "clone", barePath, work)
	runGit(t, work, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(goMod1), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "go.mod")
	runGit(t, work, "commit", "-m", "add deps")
	sha1 := runGit(t, work, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(goMod2), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "commit", "-am", "bump cobra")
	sha2 := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	ps := pkgs.Open(index.Options{})
	t.Cleanup(ps.Close)
	if err := ps.Reindex(context.Background(), gst, "alice", "demo", "main",
		plumbing.ZeroHash, plumbing.NewHash(sha2)); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	srv := httptest.NewServer(api.Handler(st, gst, ps))
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, st: st, repoID: r.ID, sha1: sha1, sha2: sha2}
}

func (f *fixture) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := f.srv.Client().Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body
}

func TestHandler_Routes(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		path     string
		want     int
		contains string
	}{
		{"/api/v1/repos/alice/demo/pkgs/list", http.StatusOK, "github.com/spf13/cobra"},
		{"/api/v1/repos/alice/demo/pkgs/list?ref=" + f.sha1, http.StatusOK, "v1.8.0"},
		{"/api/v1/repos/alice/demo/pkgs/list?ecosystem=golang", http.StatusOK, "github.com/spf13/cobra"},
		{"/api/v1/repos/alice/demo/pkgs/list?ecosystem=npm", http.StatusOK, "[]"},
		{"/api/v1/repos/alice/demo/pkgs/blame", http.StatusOK, "github.com/spf13/cobra"},
		{"/api/v1/repos/alice/demo/pkgs/blame?ref=nope", http.StatusOK, "[]"},
		{"/api/v1/repos/alice/demo/pkgs/history/github.com%2Fspf13%2Fcobra", http.StatusOK, "v1.9.0"},
		{"/api/v1/repos/alice/demo/pkgs/history/", http.StatusBadRequest, ""},
		{"/api/v1/repos/alice/demo/pkgs/diff?from=" + f.sha1 + "&to=" + f.sha2, http.StatusOK, `"modified"`},
		{"/api/v1/repos/alice/demo/pkgs/diff?from=" + f.sha1, http.StatusBadRequest, ""},
		{"/api/v1/repos/alice/demo/pkgs/show/" + f.sha2, http.StatusOK, "cobra"},
		{"/api/v1/repos/alice/demo/pkgs/stats", http.StatusOK, `"branch"`},
		{"/api/v1/repos/alice/demo/pkgs/stats?ref=nope", http.StatusOK, `"branch":"nope"`},
		{"/api/v1/repos/alice/demo/pkgs/sbom", http.StatusOK, "cobra"},
		{"/api/v1/repos/alice/demo/pkgs/sbom?format=spdx", http.StatusOK, "cobra"},
		{"/api/v1/repos/alice/demo/pkgs/sbom?format=cyclonedx-xml", http.StatusOK, "cobra"},
		{"/api/v1/repos/alice/demo/pkgs/sbom?format=bogus", http.StatusBadRequest, ""},
		{"/api/v1/repos/nobody/nothing/pkgs/list", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := f.get(t, tc.path)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d; body:\n%s", resp.StatusCode, tc.want, body)
			}
			if tc.contains != "" && !strings.Contains(string(body), tc.contains) {
				t.Errorf("body does not contain %q:\n%s", tc.contains, body)
			}
			if tc.want == http.StatusOK && !strings.Contains(tc.path, "/sbom") {
				if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
			}
		})
	}
}

func TestHandler_List(t *testing.T) {
	f := newFixture(t)
	resp, body := f.get(t, "/api/v1/repos/alice/demo/pkgs/list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", resp.StatusCode, body)
	}
	var deps []index.Dependency
	if err := json.Unmarshal(body, &deps); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(deps) != 2 {
		t.Fatalf("want 2 deps, got %d: %s", len(deps), body)
	}
	for _, d := range deps {
		if d.Ecosystem != "golang" || d.ManifestPath != "go.mod" {
			t.Errorf("unexpected dep shape: %+v", d)
		}
	}
}

func TestHandler_Diff(t *testing.T) {
	f := newFixture(t)
	resp, body := f.get(t, "/api/v1/repos/alice/demo/pkgs/diff?from="+f.sha1+"&to="+f.sha2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", resp.StatusCode, body)
	}
	var d api.DiffResult
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(d.Added) != 0 || len(d.Removed) != 0 || len(d.Modified) != 1 {
		t.Fatalf("want 0/0/1, got %d/%d/%d: %s", len(d.Added), len(d.Removed), len(d.Modified), body)
	}
	m := d.Modified[0]
	if m.Name != "github.com/spf13/cobra" || m.FromRequirement != "v1.8.0" || m.ToRequirement != "v1.9.0" {
		t.Errorf("modified entry = %+v", m)
	}
}

func TestHandler_SBOMContentType(t *testing.T) {
	f := newFixture(t)
	cases := map[string]string{
		"":              "application/vnd.cyclonedx+json",
		"cyclonedx":     "application/vnd.cyclonedx+json",
		"spdx":          "application/spdx+json",
		"cyclonedx-xml": "application/vnd.cyclonedx+xml",
	}
	for fmt, wantCT := range cases {
		path := "/api/v1/repos/alice/demo/pkgs/sbom"
		if fmt != "" {
			path += "?format=" + fmt
		}
		resp, body := f.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d; body:\n%s", fmt, resp.StatusCode, body)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != wantCT {
			t.Errorf("%s: Content-Type = %q, want %q", fmt, ct, wantCT)
		}
	}
}

func TestHandler_IndexingHeader(t *testing.T) {
	f := newFixture(t)
	if _, err := f.st.EnqueueJob(f.repoID, pkgs.JobKind, `{}`); err != nil {
		t.Fatal(err)
	}
	resp, _ := f.get(t, "/api/v1/repos/alice/demo/pkgs/list")
	if resp.Header.Get("X-Pkgs-Indexing") != "true" {
		t.Errorf("X-Pkgs-Indexing = %q, want true", resp.Header.Get("X-Pkgs-Indexing"))
	}
}

func TestFilterDeps(t *testing.T) {
	deps := []index.Dependency{
		{Name: "a", Ecosystem: "golang", ManifestKind: "manifest"},
		{Name: "b", Ecosystem: "npm", ManifestKind: "manifest"},
		{Name: "c", Ecosystem: "golang", ManifestKind: "lockfile"},
	}
	if got := api.FilterDeps(deps, url.Values{}); len(got) != 3 {
		t.Errorf("no filter: got %d", len(got))
	}
	if got := api.FilterDeps(deps, url.Values{"ecosystem": {"golang"}}); len(got) != 2 {
		t.Errorf("ecosystem=golang: got %d", len(got))
	}
	if got := api.FilterDeps(deps, url.Values{"direct": {"true"}}); len(got) != 2 {
		t.Errorf("direct=true: got %d", len(got))
	}
	if got := api.FilterDeps(deps, url.Values{"ecosystem": {"golang"}, "direct": {"true"}}); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("ecosystem+direct: got %+v", got)
	}
}

func TestComputeDiff(t *testing.T) {
	from := []index.Dependency{
		{Name: "a", ManifestPath: "go.mod", Requirement: "v1"},
		{Name: "b", ManifestPath: "go.mod", Requirement: "v1"},
		{Name: "c", ManifestPath: "go.mod", Requirement: "v1"},
	}
	to := []index.Dependency{
		{Name: "a", ManifestPath: "go.mod", Requirement: "v1"},
		{Name: "b", ManifestPath: "go.mod", Requirement: "v2"},
		{Name: "d", ManifestPath: "go.mod", Requirement: "v1"},
	}
	d := api.ComputeDiff(from, to)
	if len(d.Added) != 1 || d.Added[0].Name != "d" {
		t.Errorf("added = %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Name != "c" {
		t.Errorf("removed = %+v", d.Removed)
	}
	if len(d.Modified) != 1 || d.Modified[0].Name != "b" || d.Modified[0].FromRequirement != "v1" || d.Modified[0].ToRequirement != "v2" {
		t.Errorf("modified = %+v", d.Modified)
	}
}
