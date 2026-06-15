// Package git serves the smart-HTTP git-upload-pack protocol over net/http.
// HTTP is anonymous-read-only: receive-pack is refused.
package git

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"

	"github.com/git-pkgs/silo/internal/gitstore"
)

const serviceUploadPack = "git-upload-pack"

// Handler returns an http.Handler serving smart-HTTP upload-pack for repos in
// st. Routes:
//
//	GET  /{owner}/{repo}.git/info/refs?service=git-upload-pack
//	POST /{owner}/{repo}.git/git-upload-pack
//
// Any receive-pack request returns 404.
func Handler(st *gitstore.Store) http.Handler {
	srv := server.NewServer(&loader{st: st})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", func(w http.ResponseWriter, r *http.Request) {
		infoRefs(srv, st, w, r)
	})
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		uploadPack(srv, st, w, r)
	})
	mux.HandleFunc("/{owner}/{repo}/git-receive-pack", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "receive-pack over HTTP is disabled; use SSH", http.StatusNotFound)
	})
	return mux
}

type loader struct{ st *gitstore.Store }

func (l *loader) Load(ep *transport.Endpoint) (storer.Storer, error) { //nolint:ireturn // implements server.Loader
	owner, name, ok := splitRepoPath(ep.Path)
	if !ok {
		return nil, transport.ErrRepositoryNotFound
	}
	repo, err := l.st.Repo(owner, name)
	if err != nil {
		return nil, err
	}
	return repo.Storer, nil
}

func resolveRepo(st *gitstore.Store, r *http.Request) (*transport.Endpoint, error) {
	owner, name, ok := splitRepoPath("/" + r.PathValue("owner") + "/" + r.PathValue("repo"))
	if !ok {
		return nil, transport.ErrRepositoryNotFound
	}
	if _, err := st.Repo(owner, name); err != nil {
		return nil, err
	}
	return transport.NewEndpoint("/" + owner + "/" + name + ".git")
}

func splitRepoPath(p string) (owner, name string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.SplitN(p, "/", 2) //nolint:mnd
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func infoRefs(srv transport.Transport, st *gitstore.Store, w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("service") != serviceUploadPack {
		http.Error(w, "only git-upload-pack is served over HTTP", http.StatusNotFound)
		return
	}
	ep, err := resolveRepo(st, r)
	if err != nil {
		notFound(w, err)
		return
	}
	sess, err := srv.NewUploadPackSession(ep, nil)
	if err != nil {
		notFound(w, err)
		return
	}
	defer func() { _ = sess.Close() }()

	ar, err := sess.AdvertisedReferencesContext(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ar.Prefix = [][]byte{
		[]byte("# service=" + serviceUploadPack),
		pktline.Flush,
	}

	w.Header().Set("Content-Type", "application/x-"+serviceUploadPack+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	if err := ar.Encode(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func uploadPack(srv transport.Transport, st *gitstore.Store, w http.ResponseWriter, r *http.Request) {
	ep, err := resolveRepo(st, r)
	if err != nil {
		notFound(w, err)
		return
	}
	sess, err := srv.NewUploadPackSession(ep, nil)
	if err != nil {
		notFound(w, err)
		return
	}
	defer func() { _ = sess.Close() }()

	body, err := requestBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = body.Close() }()

	req := packp.NewUploadPackRequest()
	if err := req.Decode(body); err != nil {
		http.Error(w, "malformed upload-pack request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decodeHaves(body, &req.UploadHaves); err != nil {
		http.Error(w, "malformed haves: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/x-"+serviceUploadPack+"-result")
	w.Header().Set("Cache-Control", "no-cache")

	resp, err := sess.UploadPack(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Close() }()
	_ = resp.Encode(w)
}

func decodeHaves(r io.Reader, h *packp.UploadHaves) error {
	s := pktline.NewScanner(r)
	for s.Scan() {
		line := bytes.TrimSuffix(s.Bytes(), []byte{'\n'})
		switch {
		case len(line) == 0:
			// flush-pkt between have batches; keep reading until done.
		case bytes.Equal(line, []byte("done")):
			return nil
		case bytes.HasPrefix(line, []byte("have ")):
			hash := plumbing.NewHash(string(line[len("have "):]))
			h.Haves = append(h.Haves, hash)
		default:
			return fmt.Errorf("unexpected line %q", line)
		}
	}
	return s.Err()
}

func requestBody(r *http.Request) (io.ReadCloser, error) {
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		return gz, nil
	}
	return r.Body, nil
}

func notFound(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusNotFound)
}
