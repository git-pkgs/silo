package pkgs_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/pkgs"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func makeStore(t *testing.T) (*gitstore.Store, string) {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gst, err := gitstore.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return gst, data
}

func makeRepoWithCommit(t *testing.T, gst *gitstore.Store, owner, repo string) plumbing.Hash {
	t.Helper()
	if _, err := gst.Init(owner, repo); err != nil {
		t.Fatal(err)
	}
	barePath, err := gst.Path(owner, repo)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "wt")
	runGit(t, filepath.Dir(work), "clone", barePath, work)
	runGit(t, work, "config", "user.email", "t@x")
	runGit(t, work, "config", "user.name", "T")
	runGit(t, work, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(`module x

go 1.26

require github.com/spf13/cobra v1.8.0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "go.mod")
	runGit(t, work, "commit", "-m", "init")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return plumbing.NewHash(strings.TrimSpace(string(out)))
}

func TestStore_ReindexAndList(t *testing.T) {
	gst, _ := makeStore(t)
	tip := makeRepoWithCommit(t, gst, "alice", "demo")

	ps := pkgs.Open(index.Options{})
	defer ps.Close()

	if err := ps.Reindex(context.Background(), gst, "alice", "demo", "main", plumbing.ZeroHash, tip); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	barePath, _ := gst.Path("alice", "demo")
	if _, err := os.Stat(filepath.Join(barePath, "pkgs.sqlite3")); err != nil {
		t.Fatalf("pkgs.sqlite3 missing: %v", err)
	}

	idx, err := ps.Index(barePath)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := idx.List("main", tip.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].Name != "github.com/spf13/cobra" {
		t.Fatalf("want cobra, got %#v", deps)
	}
}

func TestPkgsStore_LRU(t *testing.T) {
	gst, _ := makeStore(t)
	ps := pkgs.Open(index.Options{})
	defer ps.Close()
	ps.SetCap(4)

	var paths []string
	for i := 0; i < 6; i++ {
		owner := "u"
		repo := "r" + string(rune('0'+i))
		makeRepoWithCommit(t, gst, owner, repo)
		p, _ := gst.Path(owner, repo)
		paths = append(paths, p)
		if _, err := ps.Index(p); err != nil {
			t.Fatalf("index %d: %v", i, err)
		}
	}

	// Reopening one of the evicted earlier paths should succeed (proves the
	// handle was closed cleanly and the db file is reusable).
	if _, err := ps.Index(paths[0]); err != nil {
		t.Fatalf("reopen evicted: %v", err)
	}
}
