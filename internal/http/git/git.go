// Package git serves the smart-HTTP git-upload-pack protocol over net/http.
// HTTP is anonymous-read-only: receive-pack is refused.
package git

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"

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
func Handler(gst *gitstore.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", func(w http.ResponseWriter, r *http.Request) {
		infoRefs(gst, w, r)
	})
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		uploadPack(gst, w, r)
	})
	mux.HandleFunc("/{owner}/{repo}/git-receive-pack", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "receive-pack over HTTP is disabled; use SSH", http.StatusNotFound)
	})
	return mux
}

func resolveRepo(gst *gitstore.Store, r *http.Request) (storage.Storer, error) {
	owner, name, ok := splitRepoPath("/" + r.PathValue("owner") + "/" + r.PathValue("repo"))
	if !ok {
		return nil, transport.ErrRepositoryNotFound
	}
	repo, err := gst.Repo(owner, name)
	if err != nil {
		return nil, err
	}
	return repo.Storer, nil
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

func infoRefs(gst *gitstore.Store, w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("service") != serviceUploadPack {
		http.Error(w, "only git-upload-pack is served over HTTP", http.StatusNotFound)
		return
	}
	st, err := resolveRepo(gst, r)
	if err != nil {
		notFound(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+serviceUploadPack+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	if err := transport.UploadPack(r.Context(), st, http.NoBody, &nopWriteCloser{w},
		&transport.UploadPackRequest{
			GitProtocol:   r.Header.Get("Git-Protocol"),
			AdvertiseRefs: true,
			StatelessRPC:  true,
		}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func uploadPack(gst *gitstore.Store, w http.ResponseWriter, r *http.Request) {
	st, err := resolveRepo(gst, r)
	if err != nil {
		notFound(w, err)
		return
	}
	body, err := requestBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "application/x-"+serviceUploadPack+"-result")
	w.Header().Set("Cache-Control", "no-cache")

	if err := transport.UploadPack(r.Context(), st, body, &nopWriteCloser{w},
		&transport.UploadPackRequest{
			GitProtocol:  r.Header.Get("Git-Protocol"),
			StatelessRPC: true,
		}); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

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
