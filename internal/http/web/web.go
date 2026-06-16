// Package web serves silo's read-only HTML UI: repo list, refs, log, commit,
// and the gittuf RSL and policy viewers.
package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/git-pkgs/silo/internal/gitstore"
	gt "github.com/git-pkgs/silo/internal/gittuf"
	"github.com/git-pkgs/silo/internal/store"
)

// Handler returns the web UI router. It does not handle the *.git/ transport
// paths; mount it after the git handler in serve.go. forgeKeyID is the SHA256
// fingerprint of silo's witness key, used to label forge-signed RSL entries.
func Handler(st *store.Store, gst *gitstore.Store, baseURL, forgeKeyID string) http.Handler {
	tmpl, err := loadTemplates()
	if err != nil {
		panic(fmt.Sprintf("web: parse templates: %v", err))
	}
	h := &handler{st: st, gst: gst, baseURL: baseURL, forgeKeyID: forgeKeyID, t: tmpl}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /{owner}/{repo}", h.repo)
	mux.HandleFunc("GET /{owner}/{repo}/{$}", h.repo)
	mux.HandleFunc("GET /{owner}/{repo}/log/{ref...}", h.log)
	mux.HandleFunc("GET /{owner}/{repo}/commit/{sha}", h.commit)
	mux.HandleFunc("GET /{owner}/{repo}/rsl", h.rsl)
	mux.HandleFunc("GET /{owner}/{repo}/policy", h.policy)
	return mux
}

type handler struct {
	st         *store.Store
	gst        *gitstore.Store
	baseURL    string
	forgeKeyID string
	t          map[string]*template.Template
}

type page struct {
	Repo    string
	BaseURL string
	Active  string
	Ref     string
	Refs    []refRow
}

func (h *handler) page(gr *git.Repository, repoPath, active, ref string) page {
	return page{
		Repo:    repoPath,
		BaseURL: h.baseURL,
		Active:  active,
		Ref:     ref,
		Refs:    listRefs(gr),
	}
}

func (h *handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.t[name].ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) open(w http.ResponseWriter, r *http.Request) (*git.Repository, string, string, bool) {
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	repo, err := h.gst.Repo(owner, name)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", "", false
	}
	path, _ := h.gst.Path(owner, name)
	return repo, owner + "/" + name, path, true
}

type repoRow struct {
	Path     string
	RefCount int
	LastRSL  string
}

func (h *handler) index(w http.ResponseWriter, _ *http.Request) {
	repos, err := h.st.ListRepos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]repoRow, 0, len(repos))
	for _, rp := range repos {
		row := repoRow{Path: rp.Owner + "/" + rp.Name}
		if gr, err := h.gst.Repo(rp.Owner, rp.Name); err == nil {
			it, _ := gr.References()
			if it != nil {
				_ = it.ForEach(func(*plumbing.Reference) error { row.RefCount++; return nil })
			}
			if ref, err := gr.Reference(plumbing.ReferenceName(gt.RSLRef), true); err == nil {
				if c, err := gr.CommitObject(ref.Hash()); err == nil {
					row.LastRSL = ago(c.Committer.When)
				}
			}
		}
		rows = append(rows, row)
	}
	h.render(w, "index", struct {
		page
		Repos []repoRow
	}{page{BaseURL: h.baseURL}, rows})
}

type refRow struct{ Name, Hash string }

func listRefs(gr *git.Repository) []refRow {
	var refs []refRow
	it, _ := gr.References()
	if it != nil {
		_ = it.ForEach(func(ref *plumbing.Reference) error {
			if ref.Type() == plumbing.HashReference {
				refs = append(refs, refRow{Name: ref.Name().String(), Hash: ref.Hash().String()})
			}
			return nil
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs
}

func (h *handler) repo(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, _, ok := h.open(w, r)
	if !ok {
		return
	}
	h.render(w, "repo", struct {
		page
		Readme template.HTML
	}{h.page(gr, repoPath, "overview", ""), readReadme(gr)})
}

type commitRow struct{ Hash, Author, When, Subject string }

const logPageSize = 50

func (h *handler) log(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, _, ok := h.open(w, r)
	if !ok {
		return
	}
	refName := r.PathValue("ref")
	from, err := gr.ResolveRevision(plumbing.Revision(refName))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if a := r.URL.Query().Get("after"); a != "" {
		hh := plumbing.NewHash(a)
		from = &hh
	}
	iter, err := gr.Log(&git.LogOptions{From: *from})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rows []commitRow
	var next string
	_ = iter.ForEach(func(c *object.Commit) error {
		if len(rows) >= logPageSize {
			next = c.Hash.String()
			return errors.New("stop")
		}
		rows = append(rows, commitRow{
			Hash:    c.Hash.String(),
			Author:  c.Author.Name,
			When:    c.Author.When.Format(time.DateOnly),
			Subject: firstLine(c.Message),
		})
		return nil
	})
	h.render(w, "log", struct {
		page
		Commits []commitRow
		Next    string
	}{h.page(gr, repoPath, "log", refName), rows, next})
}

func (h *handler) commit(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	c, err := gr.CommitObject(plumbing.NewHash(r.PathValue("sha")))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var parents, files []string
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	if st, err := c.Stats(); err == nil {
		for _, f := range st {
			files = append(files, f.Name)
		}
	}
	h.render(w, "commit", struct {
		page
		Hash, Author, When, Message, Signer string
		Parents, Files                      []string
		RSL                                 []rslRow
		Diff                                template.HTML
	}{
		h.page(gr, repoPath, "log", ""),
		c.Hash.String(), c.Author.String(), c.Author.When.Format(time.RFC1123),
		c.Message, gt.SignerFingerprint(c.Signature), parents, files,
		h.rslForCommit(r, gr, fsPath, c.Hash.String()),
		commitDiff(c),
	})
}

const diffMaxBytes = 256 << 10

func commitDiff(c *object.Commit) template.HTML {
	to, err := c.Tree()
	if err != nil {
		return ""
	}
	var from *object.Tree
	if p, err := c.Parent(0); err == nil {
		from, _ = p.Tree()
	}
	changes, err := object.DiffTree(from, to)
	if err != nil {
		return ""
	}
	patch, err := changes.Patch()
	if err != nil {
		return ""
	}
	s := patch.String()
	if s == "" {
		return ""
	}
	return renderDiff(s)
}

func renderDiff(s string) template.HTML {
	if len(s) > diffMaxBytes {
		s = s[:diffMaxBytes] + "\n… diff truncated\n"
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		esc := template.HTMLEscapeString(line)
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "@@"):
			b.WriteString(`<span class="h">` + esc + "</span>\n")
		case strings.HasPrefix(line, "+"):
			b.WriteString(`<span class="a">` + esc + "</span>\n")
		case strings.HasPrefix(line, "-"):
			b.WriteString(`<span class="d">` + esc + "</span>\n")
		default:
			b.WriteString(esc + "\n")
		}
	}
	return template.HTML(b.String()) // #nosec G203 -- every line is HTMLEscapeString'd
}

// rslForCommit returns RSL reference entries whose target is sha, each followed
// by any annotation that points at that entry.
func (h *handler) rslForCommit(r *http.Request, gr *git.Repository, fsPath, sha string) []rslRow {
	entries, err := gt.WalkRSL(r.Context(), gr)
	if err != nil {
		return nil
	}
	annot := map[string][]gt.RSLEntry{}
	for _, e := range entries {
		if e.Kind == "annotation" && e.AnnotatesID != "" {
			annot[e.AnnotatesID] = append(annot[e.AnnotatesID], e)
		}
	}
	gtr, _ := gt.Open(fsPath)
	var names map[string]string
	if gtr != nil {
		ps, _ := gtr.Policy(r.Context())
		names = h.signerNames(ps)
	}
	var rows []rslRow
	for _, e := range entries {
		if e.Kind != "reference" || e.TargetID != sha {
			continue
		}
		row := rslRow{RSLEntry: e, Age: ago(e.Timestamp), SignerName: names[e.SignerKeyID]}
		if gtr != nil && !gt.IsGittufRef(e.Ref) {
			if verr := gtr.VerifyRef(r.Context(), e.Ref); verr == nil {
				row.Verified = true
			} else {
				row.VerifyErr = verr.Error()
			}
		}
		rows = append(rows, row)
		for _, a := range annot[e.ID] {
			rows = append(rows, rslRow{RSLEntry: a, Age: ago(a.Timestamp), SignerName: names[a.SignerKeyID]})
		}
	}
	return rows
}

type rslRow struct {
	gt.RSLEntry
	Age        string
	Verified   bool
	VerifyErr  string
	SignerName string
}

// signerNames returns fingerprint → display-name built from policy principals
// plus the forge's own key as "silo". A key in policy under multiple principals
// gets the first name seen.
func (h *handler) signerNames(ps *gt.PolicySummary) map[string]string {
	m := map[string]string{}
	if h.forgeKeyID != "" {
		m[h.forgeKeyID] = "silo"
	}
	if ps != nil {
		for name, keys := range ps.Principals {
			for _, k := range keys {
				if _, ok := m[k]; !ok {
					m[k] = name
				}
			}
		}
	}
	return m
}

func (h *handler) rsl(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	entries, err := gt.WalkRSL(r.Context(), gr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gtr, _ := gt.Open(fsPath)
	var names map[string]string
	if gtr != nil {
		ps, _ := gtr.Policy(r.Context())
		names = h.signerNames(ps)
	}
	verified := map[string]error{}
	rows := make([]rslRow, 0, len(entries))
	for _, e := range entries {
		row := rslRow{RSLEntry: e, Age: ago(e.Timestamp), SignerName: names[e.SignerKeyID]}
		if e.Kind == "reference" && e.Ref != "" && !gt.IsGittufRef(e.Ref) && gtr != nil {
			verr, seen := verified[e.Ref]
			if !seen {
				verr = gtr.VerifyRef(r.Context(), e.Ref)
				verified[e.Ref] = verr
			}
			if verr == nil {
				row.Verified = true
			} else {
				row.VerifyErr = verr.Error()
			}
		}
		rows = append(rows, row)
	}
	h.render(w, "rsl", struct {
		page
		Entries []rslRow
	}{h.page(gr, repoPath, "rsl", ""), rows})
}

func (h *handler) policy(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	gtr, err := gt.Open(fsPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ps, err := gtr.Policy(r.Context())
	if err != nil {
		ps = &gt.PolicySummary{}
	}
	h.render(w, "policy", struct {
		page
		Policy *gt.PolicySummary
	}{h.page(gr, repoPath, "policy", ""), ps})
}

func readReadme(gr *git.Repository) template.HTML {
	head, err := gr.Head()
	if err != nil {
		return ""
	}
	c, err := gr.CommitObject(head.Hash())
	if err != nil {
		return ""
	}
	for _, name := range []string{"README.md", "README", "readme.md"} {
		f, err := c.File(name)
		if err != nil {
			continue
		}
		rd, err := f.Reader()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rd)
		_ = rd.Close()
		return template.HTML("<pre class=\"readme\">" + template.HTMLEscapeString(string(b)) + "</pre>") // #nosec G203 -- escaped
	}
	return ""
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format(time.DateOnly)
	}
}
