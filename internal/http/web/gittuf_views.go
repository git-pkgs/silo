package web

import (
	"net/http"
	"sort"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

// rslRef renders the RSL filtered to entries for one ref (plus annotations on
// those entries).
func (h *handler) rslRef(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	want := r.PathValue("ref")
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
	keepIDs := map[string]bool{}
	for _, e := range entries {
		if e.Kind == gt.KindReference && e.Ref == want {
			keepIDs[e.ID] = true
		}
	}
	var rows []rslRow
	for _, e := range entries {
		keep := (e.Kind == gt.KindReference && e.Ref == want) ||
			(e.Kind == gt.KindAnnotation && keepIDs[e.AnnotatesID])
		if !keep {
			continue
		}
		row := rslRow{RSLEntry: e, Age: ago(e.Timestamp), SignerName: names[e.SignerKeyID]}
		if e.Kind == gt.KindReference && gtr != nil && !gt.IsGittufRef(e.Ref) {
			if verr := gtr.VerifyRef(r.Context(), e.Ref); verr == nil {
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
		Filter  string
	}{h.page(r, gr, repoPath, fsPath, "rsl", want), rows, want})
}

// principal shows one principal's keys, the rules naming them, and the RSL
// entries any of those keys signed.
func (h *handler) principal(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	gtr, err := gt.Open(fsPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ps, _ := gtr.Policy(r.Context())
	names := h.signerNames(ps)
	keys := map[string]bool{}
	if ps != nil {
		for _, k := range ps.Principals[id] {
			keys[k] = true
		}
	}
	if len(keys) == 0 && id != forgePrincipal {
		http.NotFound(w, r)
		return
	}
	if id == forgePrincipal {
		keys[h.forgeKeyID] = true
	}
	var rules []gt.Rule
	if ps != nil {
		for _, rule := range ps.Rules {
			for _, p := range rule.Principals {
				if p == id {
					rules = append(rules, rule)
					break
				}
			}
		}
	}
	entries, _ := gt.WalkRSL(r.Context(), gr)
	var signed []rslRow
	for _, e := range entries {
		if keys[e.SignerKeyID] {
			signed = append(signed, rslRow{RSLEntry: e, Age: ago(e.Timestamp), SignerName: names[e.SignerKeyID]})
		}
	}
	var keyList []string
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)
	h.render(w, "principal", struct {
		page
		ID     string
		Keys   []string
		Rules  []gt.Rule
		Signed []rslRow
	}{h.page(r, gr, repoPath, fsPath, "policy", ""), id, keyList, rules, signed})
}

// attestations lists the contents of refs/gittuf/attestations as a tree.
func (h *handler) attestations(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	type att struct{ Path, Hash, Size string }
	var rows []att
	if ref, err := gr.Reference(plumbing.ReferenceName(gt.AttestationsRef), true); err == nil {
		if c, err := gr.CommitObject(ref.Hash()); err == nil {
			if t, err := c.Tree(); err == nil {
				_ = t.Files().ForEach(func(f *object.File) error {
					rows = append(rows, att{Path: f.Name, Hash: f.Hash.String(), Size: humanSize(f.Size)})
					return nil
				})
			}
		}
	}
	h.render(w, "attestations", struct {
		page
		Rows []att
	}{h.page(r, gr, repoPath, fsPath, "rsl", ""), rows})
}

// hooks lists in-policy Lua hooks.
func (h *handler) hooks(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	gtr, err := gt.Open(fsPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hs, _ := gtr.Hooks(r.Context())
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].Stage != hs[j].Stage {
			return hs[i].Stage < hs[j].Stage
		}
		return hs[i].Name < hs[j].Name
	})
	h.render(w, "hooks", struct {
		page
		Hooks []gt.Hook
	}{h.page(r, gr, repoPath, fsPath, "policy", ""), hs})
}

type activityRow struct {
	Repo string
	rslRow
}

// activity shows recent RSL entries across all repos.
func (h *handler) activity(w http.ResponseWriter, r *http.Request) {
	repos, err := h.st.ListRepos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rows []activityRow
	for _, rp := range repos {
		gr, err := h.gst.Repo(rp.Owner, rp.Name)
		if err != nil {
			continue
		}
		fsPath, _ := h.gst.Path(rp.Owner, rp.Name)
		var names map[string]string
		if gtr, err := gt.Open(fsPath); err == nil {
			ps, _ := gtr.Policy(r.Context())
			names = h.signerNames(ps)
		}
		entries, _ := gt.WalkRSL(r.Context(), gr)
		for _, e := range entries {
			rows = append(rows, activityRow{
				Repo:   rp.Owner + "/" + rp.Name,
				rslRow: rslRow{RSLEntry: e, Age: ago(e.Timestamp), SignerName: names[e.SignerKeyID]},
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Timestamp.After(rows[j].Timestamp) })
	const limit = 200
	if len(rows) > limit {
		rows = rows[:limit]
	}
	h.render(w, "activity", struct {
		page
		Rows []activityRow
	}{page{BaseURL: h.baseURL}, rows})
}
