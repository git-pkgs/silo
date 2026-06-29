// Package api serves /api/v1/repos/{owner}/{repo}/pkgs/* — the silo-specific
// dependency index over its per-repo pkgs.sqlite3. Shapes match
// `git pkgs <cmd> --format=json` field-for-field so the CLI's output structs
// (re-exported by git-pkgs/index) can be reused as the response payloads.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/sbom"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/pkgs"
	"github.com/git-pkgs/silo/internal/store"
)

// Handler builds the /api/v1 mux. Pass it the silo-shared gitstore, the
// in-process pkgs index, and the metadata store; the handler does its own
// JSON encoding and never touches the templates.
func Handler(st *store.Store, gst *gitstore.Store, ps *pkgs.Store) http.Handler {
	mux := http.NewServeMux()
	h := &handler{st: st, gst: gst, ps: ps}
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/list", h.list)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/blame", h.blame)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/history/{name...}", h.history)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/diff", h.diff)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/show/{sha}", h.show)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/stats", h.stats)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/pkgs/sbom", h.sbom)
	return mux
}

type handler struct {
	st  *store.Store
	gst *gitstore.Store
	ps  *pkgs.Store
}

func (h *handler) resolve(w http.ResponseWriter, r *http.Request) (*index.Index, *store.Repo, bool) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	repoPath, err := h.gst.Path(owner, repo)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, nil, false
	}
	if _, err := h.gst.Repo(owner, repo); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, nil, false
	}
	dbRepo, err := h.st.RepoByPath(owner, repo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return nil, nil, false
		}
	}
	idx, err := h.ps.Index(repoPath)
	if err != nil {
		writeServiceUnavailable(w, "open index")
		return nil, nil, false
	}
	return idx, dbRepo, true
}

func (h *handler) maybeMarkIndexing(w http.ResponseWriter, dbRepo *store.Repo) {
	if dbRepo == nil {
		return
	}
	pending, _ := h.st.HasPendingOrRunning(dbRepo.ID, pkgs.JobKind)
	if pending {
		w.Header().Set("X-Pkgs-Indexing", "true")
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	if hash, err := idx.Repo().ResolveRevision(plumbing.Revision(ref)); err == nil {
		ref = hash.String()
	}
	deps, err := idx.List("main", ref)
	if err != nil {
		writeServiceUnavailable(w, "list")
		return
	}
	deps = filterDeps(deps, r.URL.Query())
	if deps == nil {
		deps = []index.Dependency{}
	}
	writeJSON(w, deps)
}

func filterDeps(deps []index.Dependency, q url.Values) []index.Dependency {
	eco := q.Get("ecosystem")
	direct := q.Get("direct") == "true"
	if eco == "" && !direct {
		return deps
	}
	out := deps[:0:0]
	for _, d := range deps {
		if eco != "" && d.Ecosystem != eco {
			continue
		}
		if direct && d.ManifestKind != "manifest" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (h *handler) blame(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	info, err := idx.Branch(branch)
	if err != nil {
		writeJSON(w, []index.BlameEntry{})
		return
	}
	rows, err := idx.Blame(index.BlameOptions{BranchID: info.ID, Ecosystem: r.URL.Query().Get("ecosystem")})
	if err != nil {
		writeServiceUnavailable(w, "blame")
		return
	}
	writeJSON(w, rows)
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil || name == "" {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	info, err := idx.Branch(branch)
	if err != nil {
		writeJSON(w, []index.HistoryEntry{})
		return
	}
	rows, err := idx.History(index.HistoryOptions{
		BranchID:    info.ID,
		PackageName: name,
		Ecosystem:   r.URL.Query().Get("ecosystem"),
	})
	if err != nil {
		writeServiceUnavailable(w, "history")
		return
	}
	writeJSON(w, rows)
}

// DiffEntry mirrors cmd/diff.go shape until the CLI is rewired onto the
// index package (SPEC-pkgs.md R9). One row per added/removed/updated dep.
type DiffEntry struct {
	Name            string `json:"name"`
	Ecosystem       string `json:"ecosystem,omitempty"`
	ManifestPath    string `json:"manifest_path"`
	DependencyType  string `json:"dependency_type,omitempty"`
	FromRequirement string `json:"from_requirement,omitempty"`
	ToRequirement   string `json:"to_requirement,omitempty"`
}

// DiffResult mirrors cmd/diff.go.
type DiffResult struct {
	Added    []DiffEntry `json:"added,omitempty"`
	Modified []DiffEntry `json:"modified,omitempty"`
	Removed  []DiffEntry `json:"removed,omitempty"`
}

func (h *handler) diff(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		http.Error(w, "from and to required", http.StatusBadRequest)
		return
	}
	if h, err := idx.Repo().ResolveRevision(plumbing.Revision(from)); err == nil {
		from = h.String()
	}
	if h, err := idx.Repo().ResolveRevision(plumbing.Revision(to)); err == nil {
		to = h.String()
	}
	fromDeps, err := idx.List("main", from)
	if err != nil {
		writeServiceUnavailable(w, "diff from")
		return
	}
	toDeps, err := idx.List("main", to)
	if err != nil {
		writeServiceUnavailable(w, "diff to")
		return
	}
	writeJSON(w, computeDiff(fromDeps, toDeps))
}

func computeDiff(from, to []index.Dependency) DiffResult {
	idxOf := func(deps []index.Dependency) map[string]index.Dependency {
		m := make(map[string]index.Dependency, len(deps))
		for _, d := range deps {
			m[d.ManifestPath+":"+d.Name] = d
		}
		return m
	}
	fromMap := idxOf(from)
	toMap := idxOf(to)

	var out DiffResult
	for k, t := range toMap {
		f, ok := fromMap[k]
		if !ok {
			out.Added = append(out.Added, DiffEntry{
				Name: t.Name, Ecosystem: t.Ecosystem, ManifestPath: t.ManifestPath,
				DependencyType: t.DependencyType, ToRequirement: t.Requirement,
			})
			continue
		}
		if f.Requirement != t.Requirement {
			out.Modified = append(out.Modified, DiffEntry{
				Name: t.Name, Ecosystem: t.Ecosystem, ManifestPath: t.ManifestPath,
				DependencyType: t.DependencyType, FromRequirement: f.Requirement, ToRequirement: t.Requirement,
			})
		}
	}
	for k, f := range fromMap {
		if _, ok := toMap[k]; !ok {
			out.Removed = append(out.Removed, DiffEntry{
				Name: f.Name, Ecosystem: f.Ecosystem, ManifestPath: f.ManifestPath,
				DependencyType: f.DependencyType, FromRequirement: f.Requirement,
			})
		}
	}
	return out
}

func (h *handler) show(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	sha := r.PathValue("sha")
	changes, err := idx.Show(sha)
	if err != nil {
		writeServiceUnavailable(w, "show")
		return
	}
	if changes == nil {
		_, _ = w.Write([]byte("[]\n"))
		return
	}
	writeJSON(w, changes)
}

func (h *handler) stats(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	info, err := idx.Branch(branch)
	if err != nil {
		writeJSON(w, &index.Stats{Branch: branch})
		return
	}
	s, err := idx.StatsFor(index.StatsOptions{BranchID: info.ID})
	if err != nil {
		writeServiceUnavailable(w, "stats")
		return
	}
	writeJSON(w, s)
}

func (h *handler) sbom(w http.ResponseWriter, r *http.Request) {
	idx, dbRepo, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.maybeMarkIndexing(w, dbRepo)
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	if hash, err := idx.Repo().ResolveRevision(plumbing.Revision(ref)); err == nil {
		ref = hash.String()
	}
	deps, err := idx.List("main", ref)
	if err != nil {
		writeServiceUnavailable(w, "sbom")
		return
	}
	doc := buildSBOM(deps)

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "cyclonedx"
	}
	var fmtSel sbom.Format
	switch format {
	case "cyclonedx", "cyclonedx-json":
		fmtSel = sbom.FormatCycloneDXJSON
		w.Header().Set("Content-Type", "application/vnd.cyclonedx+json")
	case "spdx", "spdx-json":
		fmtSel = sbom.FormatSPDXJSON
		w.Header().Set("Content-Type", "application/spdx+json")
	case "cyclonedx-xml":
		fmtSel = sbom.FormatCycloneDXXML
		w.Header().Set("Content-Type", "application/vnd.cyclonedx+xml")
	default:
		http.Error(w, "unknown format", http.StatusBadRequest)
		return
	}
	if err := sbom.Encode(w, doc, fmtSel); err != nil {
		writeServiceUnavailable(w, "encode sbom")
	}
}

func buildSBOM(deps []index.Dependency) *sbom.SBOM {
	s := sbom.New(sbom.TypeUnknown)
	for _, d := range deps {
		var refs []sbom.ExternalRef
		if d.PURL != "" {
			refs = append(refs, sbom.ExternalRef{Type: "purl", Locator: d.PURL})
		}
		s.AddPackage(sbom.Package{
			Name:         d.Name,
			Version:      d.Requirement,
			ExternalRefs: refs,
		})
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeServiceUnavailable(w http.ResponseWriter, what string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "dependencies unavailable: " + what})
}
