// Package web serves silo's read-only HTML UI: repo list, refs, log, commit,
// and the gittuf RSL and policy viewers.
package web

import (
	"encoding/json"
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
	mux.HandleFunc("GET /{owner}/{repo}/tree/{rest...}", h.tree)
	mux.HandleFunc("GET /{owner}/{repo}/blob/{rest...}", h.blob)
	mux.HandleFunc("GET /{owner}/{repo}/raw/{rest...}", h.raw)
	mux.HandleFunc("GET /{owner}/{repo}/blame/{rest...}", h.blame)
	mux.HandleFunc("GET /{owner}/{repo}/history/{rest...}", h.fileHistory)
	mux.HandleFunc("GET /{owner}/{repo}/archive/{rest...}", h.archive)
	mux.HandleFunc("GET /{owner}/{repo}/contributors", h.contributors)
	mux.HandleFunc("GET /{owner}/{repo}/search/{ref...}", h.search)
	mux.HandleFunc("GET /{owner}/{repo}/compare/{spec...}", h.compare)
	mux.HandleFunc("GET /{owner}/{repo}/log/{ref...}", h.log)
	mux.HandleFunc("GET /{owner}/{repo}/commit/{sha}", h.commit)
	mux.HandleFunc("GET /activity", h.activity)
	mux.HandleFunc("GET /{owner}/{repo}/rsl", h.rsl)
	mux.HandleFunc("GET /{owner}/{repo}/rsl/{ref...}", h.rslRef)
	mux.HandleFunc("GET /{owner}/{repo}/principal/{id}", h.principal)
	mux.HandleFunc("GET /{owner}/{repo}/attestations", h.attestations)
	mux.HandleFunc("GET /{owner}/{repo}/hooks", h.hooks)
	mux.HandleFunc("GET /{owner}/{repo}/policy", h.policy)
	mux.HandleFunc("GET /{owner}/{repo}/policy/history", h.policyHistory)
	mux.HandleFunc("GET /{owner}/{repo}/verify", h.verify)
	mux.HandleFunc("GET /{owner}/{repo}/branches", h.branches)
	mux.HandleFunc("GET /{owner}/{repo}/tags", h.tags)
	return mux
}

const forgePrincipal = "silo"

type handler struct {
	st         *store.Store
	gst        *gitstore.Store
	baseURL    string
	forgeKeyID string
	t          map[string]*template.Template
}

type navLink struct{ Label, Href, Icon string }

type page struct {
	Repo      string
	BaseURL   string
	Active    string
	SubActive string
	SubNav    []navLink
	Ref       string
	Refs      []refRow
	RefGroups refGroups
	VerifyBad int
}

func (h *handler) page(r *http.Request, gr *git.Repository, repoPath, fsPath, active, ref string) page {
	refs := listRefs(gr)
	top, sub, _ := strings.Cut(active, "/")
	return page{
		Repo:      repoPath,
		BaseURL:   h.baseURL,
		Active:    top,
		SubActive: sub,
		SubNav:    subNavFor(top, repoPath),
		Ref:       ref,
		Refs:      refs,
		RefGroups: groupRefs(refs),
		VerifyBad: h.verifyBadge(r.Context(), gr, fsPath),
	}
}

func subNavFor(top, repo string) []navLink {
	p := "/" + repo
	switch top {
	case "commits":
		return []navLink{
			{"log", p + "/log/HEAD", "history"},
			{"branches", p + "/branches", "git-branch"},
			{"tags", p + "/tags", "tag"},
			{"contributors", p + "/contributors", "users"},
		}
	case "policy":
		return []navLink{
			{"rules", p + "/policy", "shield"},
			{"history", p + "/policy/history", "scroll"},
			{"hooks", p + "/hooks", "braces"},
			{"attestations", p + "/attestations", "file-check"},
		}
	}
	return nil
}

func (h *handler) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(data)
		return
	}
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
	Path, DefaultRef, Description     string
	Branches, Tags, RSLEntries, Rules int
	VerifyBad                         int
	LastRSL, LastSigner               string
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	repos, err := h.st.ListRepos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]repoRow, 0, len(repos))
	for _, rp := range repos {
		rows = append(rows, h.repoRow(r, rp.Owner, rp.Name))
	}
	h.render(w, r, "index", struct {
		page
		Repos []repoRow
	}{page{BaseURL: h.baseURL}, rows})
}

func (h *handler) repoRow(r *http.Request, owner, name string) repoRow {
	row := repoRow{Path: owner + "/" + name}
	gr, err := h.gst.Repo(owner, name)
	if err != nil {
		return row
	}
	row.DefaultRef = shortRef(defaultBranch(gr))
	g := groupRefs(listRefs(gr))
	row.Branches, row.Tags = len(g.Heads), len(g.Tags)
	if entries, _ := gt.WalkRSL(r.Context(), gr); len(entries) > 0 {
		row.RSLEntries = len(entries)
		row.LastRSL = ago(entries[0].Timestamp)
		row.LastSigner = entries[0].SignerKeyID
	}
	fsPath, _ := h.gst.Path(owner, name)
	if gtr, err := gt.Open(fsPath); err == nil {
		if ps, _ := gtr.Policy(r.Context()); ps != nil {
			row.Rules = len(ps.Rules)
			if n := h.signerNames(ps)[row.LastSigner]; n != "" {
				row.LastSigner = n
			}
		}
		_, row.VerifyBad = verifyRefs(r.Context(), gr, gtr)
	}
	row.Description = readmeDescription(gr)
	return row
}

func readmeDescription(gr *git.Repository) string {
	head, err := gr.Head()
	if err != nil {
		return ""
	}
	c, _ := gr.CommitObject(head.Hash())
	for _, name := range readmeNames {
		f, err := c.File(name)
		if err != nil {
			continue
		}
		s, _ := f.Contents()
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			const maxDesc = 160
			if len(line) > maxDesc {
				line = line[:maxDesc] + "…"
			}
			return line
		}
	}
	return ""
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
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	defaultRef := defaultBranch(gr)
	var entries []treeEntry
	if hh, err := gr.ResolveRevision(plumbing.Revision(defaultRef)); err == nil {
		if c, err := gr.CommitObject(*hh); err == nil {
			if t, err := c.Tree(); err == nil {
				entries = treeEntries(t)
			}
		}
	}
	h.render(w, r, "repo", struct {
		page
		DefaultRef string
		Entries    []treeEntry
		Readme     template.HTML
	}{h.page(r, gr, repoPath, fsPath, "code", defaultRef), defaultRef, entries, readReadme(gr)})
}

var errStop = errors.New("stop")

type commitRow struct{ Hash, Author, When, Subject string }

type fileStat struct {
	Name     string
	Add, Del int
}

const logPageSize = 50

func (h *handler) log(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
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
			return errStop
		}
		rows = append(rows, commitRow{
			Hash:    c.Hash.String(),
			Author:  c.Author.Name,
			When:    c.Author.When.Format(time.DateOnly),
			Subject: firstLine(c.Message),
		})
		return nil
	})
	h.render(w, r, "log", struct {
		page
		Commits []commitRow
		Next    string
	}{h.page(r, gr, repoPath, fsPath, "commits/log", refName), rows, next})
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
	var parents []string
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	var files []fileStat
	if st, err := c.Stats(); err == nil {
		for _, f := range st {
			files = append(files, fileStat{Name: f.Name, Add: f.Addition, Del: f.Deletion})
		}
	}
	h.render(w, r, "commit", struct {
		page
		Hash, Author, When, Message, Signer string
		Parents                             []string
		Files                               []fileStat
		RSL                                 []rslRow
		Diff                                template.HTML
	}{
		h.page(r, gr, repoPath, fsPath, "commits/log", ""),
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
		if e.Kind == gt.KindAnnotation && e.AnnotatesID != "" {
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
		if e.Kind != gt.KindReference || e.TargetID != sha {
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
		m[h.forgeKeyID] = forgePrincipal
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
		if e.Kind == gt.KindReference && e.Ref != "" && !gt.IsGittufRef(e.Ref) && gtr != nil {
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
	h.render(w, r, "rsl", struct {
		page
		Entries []rslRow
		Filter  string
	}{h.page(r, gr, repoPath, fsPath, "rsl", ""), rows, ""})
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
	h.render(w, r, "policy", struct {
		page
		Policy *gt.PolicySummary
	}{h.page(r, gr, repoPath, fsPath, "policy/rules", ""), ps})
}

var readmeNames = []string{"README.md", "README.org", "README", "readme.md"} //nolint:goconst

func readReadme(gr *git.Repository) template.HTML {
	head, err := gr.Head()
	if err != nil {
		return ""
	}
	c, err := gr.CommitObject(head.Hash())
	if err != nil {
		return ""
	}
	for _, name := range readmeNames {
		f, err := c.File(name)
		if err != nil {
			continue
		}
		rd, err := f.Reader()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rd, blobMaxBytes))
		_ = rd.Close()
		if html := renderMarkup(name, b); html != "" {
			return html
		}
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
