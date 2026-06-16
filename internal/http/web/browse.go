package web

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
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

func treeEntries(t *object.Tree) []treeEntry {
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
	return entries
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
	entries := treeEntries(t)
	var readme template.HTML
	if p == "" {
		readme = readReadme(gr)
	}
	h.render(w, r, "tree", struct {
		page
		Path    string
		Crumbs  []crumb
		Entries []treeEntry
		Readme  template.HTML
	}{h.page(r, gr, repoPath, fsPath, "code", ref), p, crumbs(p), entries, readme})
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
	h.render(w, r, "blob", struct {
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
		h.page(r, gr, repoPath, fsPath, "code", ref),
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

func (h *handler) fileHistory(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	ref, c, p, ok := splitRefPath(gr, r.PathValue("rest"))
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	iter, err := gr.Log(&git.LogOptions{
		From:       c.Hash,
		PathFilter: func(s string) bool { return s == p },
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rows []commitRow
	_ = iter.ForEach(func(cc *object.Commit) error {
		if len(rows) >= logPageSize {
			return errStop
		}
		rows = append(rows, commitRow{
			Hash:    cc.Hash.String(),
			Author:  cc.Author.Name,
			When:    cc.Author.When.Format(time.DateOnly),
			Subject: firstLine(cc.Message),
		})
		return nil
	})
	h.render(w, r, "history", struct {
		page
		Path    string
		Crumbs  []crumb
		Commits []commitRow
	}{h.page(r, gr, repoPath, fsPath, "commits/log", ref), p, crumbs(p), rows})
}

type blameLine struct {
	Hash, Author, When, Text string
}

func (h *handler) blame(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	ref, c, p, ok := splitRefPath(gr, r.PathValue("rest"))
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	res, err := git.Blame(c, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lines := make([]blameLine, 0, len(res.Lines))
	for _, l := range res.Lines {
		lines = append(lines, blameLine{
			Hash: l.Hash.String(), Author: l.AuthorName,
			When: l.Date.Format(time.DateOnly), Text: l.Text,
		})
	}
	h.render(w, r, "blame", struct {
		page
		Path   string
		Crumbs []crumb
		Lines  []blameLine
	}{h.page(r, gr, repoPath, fsPath, "code", ref), p, crumbs(p), lines})
}

const tarFilePerm = 0o644

func (h *handler) archive(w http.ResponseWriter, r *http.Request) {
	gr, _, _, ok := h.open(w, r)
	if !ok {
		return
	}
	ref := strings.TrimSuffix(r.PathValue("rest"), ".tar.gz")
	hh, err := gr.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := gr.CommitObject(*hh)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := c.Tree()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.PathValue("repo") + "-" + shortRef(ref)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tar.gz"`, name))
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	_ = t.Files().ForEach(func(f *object.File) error {
		mode := int64(tarFilePerm)
		if m, err := f.Mode.ToOSFileMode(); err == nil {
			mode = int64(m.Perm())
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name + "/" + f.Name, Mode: mode, Size: f.Size,
			ModTime: c.Committer.When,
		}); err != nil {
			return err
		}
		rd, err := f.Reader()
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, rd)
		_ = rd.Close()
		return err
	})
	_ = tw.Close()
	_ = gz.Close()
}

type contributor struct {
	Name, Email string
	Commits     int
}

func (h *handler) contributors(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	head, err := gr.Head()
	if err != nil {
		http.Error(w, "no HEAD", http.StatusNotFound)
		return
	}
	iter, _ := gr.Log(&git.LogOptions{From: head.Hash()})
	by := map[string]*contributor{}
	n := 0
	_ = iter.ForEach(func(c *object.Commit) error {
		if n >= aheadCap*10 {
			return errStop
		}
		n++
		k := c.Author.Email
		if by[k] == nil {
			by[k] = &contributor{Name: c.Author.Name, Email: k}
		}
		by[k].Commits++
		return nil
	})
	rows := make([]contributor, 0, len(by))
	for _, v := range by {
		rows = append(rows, *v)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Commits > rows[j].Commits })
	h.render(w, r, "contributors", struct {
		page
		Rows  []contributor
		Total int
	}{h.page(r, gr, repoPath, fsPath, "commits/contributors", ""), rows, n})
}

type searchHit struct{ Path, Hash string }

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	ref := r.PathValue("ref")
	q := strings.ToLower(r.URL.Query().Get("q"))
	hh, err := gr.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, _ := gr.CommitObject(*hh)
	t, _ := c.Tree()
	var hits []searchHit
	if q != "" {
		_ = t.Files().ForEach(func(f *object.File) error {
			if strings.Contains(strings.ToLower(f.Name), q) {
				hits = append(hits, searchHit{Path: f.Name, Hash: f.Hash.String()})
			}
			return nil
		})
	}
	h.render(w, r, "search", struct {
		page
		Q    string
		Hits []searchHit
	}{h.page(r, gr, repoPath, fsPath, "code", ref), q, hits})
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
