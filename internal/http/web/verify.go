package web

import (
	"context"
	"net/http"

	"github.com/go-git/go-git/v6"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

type verifyRow struct {
	Ref, Hash, Err string
	Covered, OK    bool
}

// verifyRefs runs VerifyRef on every non-gittuf hash ref and reports whether
// each tip is covered by an RSL reference entry.
func verifyRefs(ctx context.Context, gr *git.Repository, gtr *gt.Repo) ([]verifyRow, int) {
	covered := map[string]map[string]bool{}
	if entries, _ := gt.WalkRSL(ctx, gr); entries != nil {
		for _, e := range entries {
			if e.Kind != gt.KindReference || e.Ref == "" {
				continue
			}
			if covered[e.Ref] == nil {
				covered[e.Ref] = map[string]bool{}
			}
			covered[e.Ref][e.TargetID] = true
		}
	}
	hasPolicy := gtr != nil && gtr.HasSignedRoot()
	var rows []verifyRow
	bad := 0
	for _, r := range listRefs(gr) {
		if gt.IsGittufRef(r.Name) {
			continue
		}
		row := verifyRow{Ref: r.Name, Hash: r.Hash, Covered: covered[r.Name][r.Hash]}
		if !hasPolicy {
			row.Err = "no signed policy"
		} else if err := gtr.VerifyRef(ctx, r.Name); err != nil {
			row.Err = err.Error()
		}
		row.OK = row.Err == "" && row.Covered
		if !row.OK {
			bad++
		}
		rows = append(rows, row)
	}
	return rows, bad
}

func (h *handler) verify(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	gtr, _ := gt.Open(fsPath)
	rows, bad := verifyRefs(r.Context(), gr, gtr)
	h.render(w, "verify", struct {
		page
		Rows []verifyRow
		Bad  int
	}{h.page(r, gr, repoPath, fsPath, "verify", ""), rows, bad})
}

// verifyBadge returns the number of failing refs for the header tab. It's
// computed on demand for the page being rendered, so other handlers call it
// when building their page struct.
func (h *handler) verifyBadge(ctx context.Context, gr *git.Repository, fsPath string) int {
	gtr, _ := gt.Open(fsPath)
	_, bad := verifyRefs(ctx, gr, gtr)
	return bad
}
