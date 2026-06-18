package web

import (
	"net/http"
	"net/url"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/silo/internal/pkgs"
	"github.com/git-pkgs/silo/internal/store"
)

// depsBaseData is the shared sub-page data used by every deps_* template.
type depsBaseData struct {
	page
	Branch    string
	Indexing  bool
	Ecosystem string
}

// dependenciesList renders the per-repo dependency table at the default
// branch.
func (h *handler) dependenciesList(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	bareRepoPath, _ := h.gst.Path(owner, repo)

	idx, err := h.ps.Index(bareRepoPath)
	if err != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}

	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	ref := branch
	if hash, err := gr.ResolveRevision(plumbing.Revision(branch)); err == nil {
		ref = hash.String()
	}
	deps, _ := idx.List("main", ref)

	indexing := h.indexingFor(owner, repo)

	h.render(w, r, "deps_list", struct {
		depsBaseData
		Deps []index.Dependency
	}{
		depsBaseData{h.page(r, gr, repoPath, fsPath, "dependencies", branch), branch, indexing, ""},
		deps,
	})
}

// dependenciesBlame renders the blame table for the current branch.
func (h *handler) dependenciesBlame(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	bareRepoPath, _ := h.gst.Path(owner, repo)
	idx, err := h.ps.Index(bareRepoPath)
	if err != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	var entries []index.BlameEntry
	if info, ierr := idx.Branch(branch); ierr == nil {
		entries, _ = idx.Blame(index.BlameOptions{BranchID: info.ID})
	}

	h.render(w, r, "deps_blame", struct {
		depsBaseData
		Entries []index.BlameEntry
	}{
		depsBaseData{h.page(r, gr, repoPath, fsPath, "dependencies", branch), branch, h.indexingFor(owner, repo), ""},
		entries,
	})
}

// dependenciesStats renders ecosystem/scope counts and top-changed packages.
func (h *handler) dependenciesStats(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	bareRepoPath, _ := h.gst.Path(owner, repo)
	idx, err := h.ps.Index(bareRepoPath)
	if err != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	var stats *index.Stats
	if info, ierr := idx.Branch(branch); ierr == nil {
		stats, _ = idx.StatsFor(index.StatsOptions{BranchID: info.ID})
	}
	if stats == nil {
		stats = &index.Stats{Branch: branch}
	}

	h.render(w, r, "deps_stats", struct {
		depsBaseData
		Stats *index.Stats
	}{
		depsBaseData{h.page(r, gr, repoPath, fsPath, "dependencies", branch), branch, h.indexingFor(owner, repo), ""},
		stats,
	})
}

// dependenciesPackage renders the change history for one package, keyed on
// the PURL captured in the URL path.
func (h *handler) dependenciesPackage(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	bareRepoPath, _ := h.gst.Path(owner, repo)
	idx, err := h.ps.Index(bareRepoPath)
	if err != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}
	purl, perr := url.PathUnescape(r.PathValue("purl"))
	if perr != nil || purl == "" {
		http.NotFound(w, r)
		return
	}
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = "main"
	}
	// Derive the package name from the purl. Format: pkg:<eco>/<name>@<v>.
	name := purlPackageName(purl)
	var entries []index.HistoryEntry
	if info, ierr := idx.Branch(branch); ierr == nil {
		entries, _ = idx.History(index.HistoryOptions{
			BranchID: info.ID, PackageName: name,
		})
	}

	h.render(w, r, "deps_history", struct {
		depsBaseData
		PURL    string
		Name    string
		Entries []index.HistoryEntry
	}{
		depsBaseData{h.page(r, gr, repoPath, fsPath, "dependencies", branch), branch, h.indexingFor(owner, repo), ""},
		purl, name, entries,
	})
}

func purlPackageName(purl string) string {
	// pkg:<eco>/<name>[@version]
	const prefix = "pkg:"
	if len(purl) < len(prefix) || purl[:len(prefix)] != prefix {
		return purl
	}
	rest := purl[len(prefix):]
	if i := indexByteFrom(rest, '/'); i >= 0 {
		rest = rest[i+1:]
	}
	if i := indexByteFrom(rest, '@'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func indexByteFrom(s string, c byte) int {
	for i := range len(s) {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func (h *handler) indexingFor(owner, repo string) bool {
	r, err := h.st.RepoByPath(owner, repo)
	if err != nil {
		return false
	}
	pending, _ := h.st.HasPendingOrRunning(r.ID, pkgs.JobKind)
	return pending
}

// Compile-time guarantees that the package imports stay referenced even if
// individual fields are trimmed in future refactors.
var _ = store.JobPending
