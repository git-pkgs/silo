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
	e := h.cached(r.Context(), gr, fsPath)
	h.render(w, r, "verify", struct {
		page
		Rows []verifyRow
		Bad  int
	}{h.page(r, gr, repoPath, fsPath, "verify", ""), e.rows, e.bad})
}

// cached returns the verify+policy snapshot for a repo, computed once per
// distinct ref state and shared across handlers so page renders don't shell
// to gittuf when nothing has moved.
func (h *handler) cached(ctx context.Context, gr *git.Repository, fsPath string) verifyCacheEntry {
	return h.vc.entry(ctx, h, gr, fsPath)
}
