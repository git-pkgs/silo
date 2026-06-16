package web

import (
	"html/template"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// splitRefPath resolves the longest prefix of rest that names a revision in
// gr, returning that prefix, its commit, and the remainder as a tree path.
func splitRefPath(gr *git.Repository, rest string) (ref string, c *object.Commit, p string, ok bool) {
	rest = strings.Trim(rest, "/")
	probe := rest
	for probe != "" {
		if h, err := gr.ResolveRevision(plumbing.Revision(probe)); err == nil {
			if cc, err := gr.CommitObject(*h); err == nil {
				return probe, cc, strings.TrimPrefix(strings.TrimPrefix(rest, probe), "/"), true
			}
		}
		i := strings.LastIndexByte(probe, '/')
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	return "", nil, "", false
}

type treeEntry struct {
	Name, Hash, Mode, Size string
	IsDir                  bool
}

type crumb struct{ Name, Path string }

func crumbs(p string) []crumb {
	if p == "" {
		return nil
	}
	var out []crumb
	parts := strings.Split(p, "/")
	for i, part := range parts {
		out = append(out, crumb{Name: part, Path: strings.Join(parts[:i+1], "/")})
	}
	return out
}

func (h *handler) tree(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	ref, c, p, ok := splitRefPath(gr, r.PathValue("rest"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	root, err := c.Tree()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t := root
	if p != "" {
		if t, err = root.Tree(p); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	entries := make([]treeEntry, 0, len(t.Entries))
	for _, e := range t.Entries {
		te := treeEntry{
			Name:  e.Name,
			Hash:  e.Hash.String(),
			Mode:  e.Mode.String(),
			IsDir: e.Mode == filemode.Dir || e.Mode == filemode.Submodule,
		}
		if !te.IsDir {
			if sz, err := t.Size(e.Name); err == nil {
				te.Size = humanSize(sz)
			}
		}
		entries = append(entries, te)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	var readme template.HTML
	if p == "" {
		readme = readReadme(gr)
	}
	h.render(w, "tree", struct {
		page
		Path    string
		Crumbs  []crumb
		Entries []treeEntry
		Readme  template.HTML
	}{h.page(r, gr, repoPath, fsPath, "overview", ref), p, crumbs(p), entries, readme})
}

const blobMaxBytes = 512 << 10

func (h *handler) blob(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	ref, c, p, ok := splitRefPath(gr, r.PathValue("rest"))
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	f, err := c.File(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rd, err := f.Reader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rd.Close() }()
	data, _ := io.ReadAll(io.LimitReader(rd, blobMaxBytes+1))
	truncated := int64(len(data)) > blobMaxBytes
	if truncated {
		data = data[:blobMaxBytes]
	}
	binary := !utf8.Valid(data)

	var rendered template.HTML
	if !binary && !truncated {
		rendered = renderMarkup(p, data)
	}
	h.render(w, "blob", struct {
		page
		Path       string
		Crumbs     []crumb
		Dir        string
		Size       string
		Mode       string
		Binary     bool
		Truncated  bool
		Rendered   template.HTML
		Content    string
		LineCount  int
	}{
		h.page(r, gr, repoPath, fsPath, "overview", ref),
		p, crumbs(p), path.Dir(p), humanSize(f.Size), f.Mode.String(),
		binary, truncated, rendered, string(data), countLines(data),
	})
}

func (h *handler) raw(w http.ResponseWriter, r *http.Request) {
	gr, _, _, ok := h.open(w, r)
	if !ok {
		return
	}
	_, c, p, ok := splitRefPath(gr, r.PathValue("rest"))
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	f, err := c.File(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rd, err := f.Reader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rd.Close() }()
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, rd)
}

func humanSize(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return strconv.FormatInt(n, 10) + " B"
	case n < k*k:
		return strconv.FormatInt(n/k, 10) + " KiB"
	default:
		return strconv.FormatInt(n/(k*k), 10) + " MiB"
	}
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 1
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
