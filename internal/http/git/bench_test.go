package git

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/git-pkgs/silo/internal/gitstore"
)

func benchSeed(b *testing.B, st *gitstore.Store, owner, name string, nCommits int) {
	b.Helper()
	p, _ := st.Path(owner, name)
	if err := os.MkdirAll(p, 0o750); err != nil {
		b.Fatal(err)
	}
	repo, err := gogit.PlainInit(p, false, gogit.WithDefaultBranch("refs/heads/main"))
	if err != nil {
		b.Fatal(err)
	}
	cfg, _ := repo.Config()
	cfg.Commit.GpgSign = config.NewOptBool(false)
	if err := repo.SetConfig(cfg); err != nil {
		b.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		b.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()}
	for i := range nCommits {
		path := filepath.Join(p, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, fmt.Appendf(nil, "line %d\n", i), 0o600); err != nil {
			b.Fatal(err)
		}
		if _, err := wt.Add(fmt.Sprintf("f%d.txt", i)); err != nil {
			b.Fatal(err)
		}
		if _, err := wt.Commit(fmt.Sprintf("c%d", i), &gogit.CommitOptions{Author: sig, Committer: sig}); err != nil {
			b.Fatal(err)
		}
	}
}

func newBenchServer(b *testing.B, nCommits int) *httptest.Server {
	b.Helper()
	st, err := gitstore.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	benchSeed(b, st, "a", "r", nCommits)
	srv := httptest.NewServer(Handler(st))
	b.Cleanup(srv.Close)
	return srv
}

func BenchmarkInfoRefs(b *testing.B) {
	srv := newBenchServer(b, 100)
	url := srv.URL + "/a/r.git/info/refs?service=git-upload-pack"
	for b.Loop() {
		resp, err := srv.Client().Get(url)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkUploadPack(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			srv := newBenchServer(b, n)
			body := wantBody(b, srv, n)
			url := srv.URL + "/a/r.git/git-upload-pack"
			b.ResetTimer()
			for b.Loop() {
				req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
				resp, err := srv.Client().Do(req)
				if err != nil {
					b.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		})
	}
}

// wantBody captures the main tip via info/refs and builds a minimal
// want/done pkt-line body that requests the full pack.
func wantBody(b *testing.B, srv *httptest.Server, _ int) []byte {
	b.Helper()
	resp, err := srv.Client().Get(srv.URL + "/a/r.git/info/refs?service=git-upload-pack")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	// info/refs is pkt-line; main tip is the first 40 hex bytes after the
	// service line. Probe lazily — find the first 40-hex run.
	isHex := func(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') }
	var tip string
	for i := 0; i+40 <= len(raw); i++ {
		if !isHex(raw[i]) {
			continue
		}
		ok := true
		for j := 1; j < 40; j++ {
			if !isHex(raw[i+j]) {
				ok = false
				break
			}
		}
		if ok {
			tip = string(raw[i : i+40])
			break
		}
	}
	if tip == "" {
		b.Fatal("could not find main tip in info/refs")
	}
	// v2 wire: command=fetch, want, done, flush.
	var buf bytes.Buffer
	pkt := func(s string) { fmt.Fprintf(&buf, "%04x%s", len(s)+4, s) }
	pkt("command=fetch\n")
	buf.WriteString("0001")
	pkt("want " + tip + "\n")
	pkt("done\n")
	buf.WriteString("0000")
	return buf.Bytes()
}
