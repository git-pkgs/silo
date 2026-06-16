package web

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

type refDetail struct {
	Name, Short, Hash, Subject, When string
	Ahead, Behind                    int
	Default, Verified                bool
	VerifyErr, RuleName              string
}

func (h *handler) branches(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	def := defaultBranch(gr)
	rows := h.refDetails(r.Context(), gr, fsPath, "refs/heads/", def)
	h.render(w, r, "branches", struct {
		page
		Default string
		Rows    []refDetail
	}{h.page(r, gr, repoPath, fsPath, "branches", ""), def, rows})
}

func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	rows := h.refDetails(r.Context(), gr, fsPath, "refs/tags/", "")
	sort.Slice(rows, func(i, j int) bool { return rows[i].When > rows[j].When })
	h.render(w, r, "tags", struct {
		page
		Rows []refDetail
	}{h.page(r, gr, repoPath, fsPath, "tags", ""), rows})
}

func (h *handler) refDetails(ctx context.Context, gr *git.Repository, fsPath, prefix, def string) []refDetail {
	gtr, _ := gt.Open(fsPath)
	var defHash plumbing.Hash
	if def != "" {
		if hh, err := gr.ResolveRevision(plumbing.Revision(def)); err == nil {
			defHash = *hh
		}
	}
	var rows []refDetail
	for _, r := range listRefs(gr) {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		d := refDetail{
			Name:    r.Name,
			Short:   strings.TrimPrefix(r.Name, prefix),
			Hash:    r.Hash,
			Default: r.Name == def,
		}
		h := plumbing.NewHash(r.Hash)
		if c, err := commitFor(gr, h); err == nil {
			d.Subject = firstLine(c.Message)
			d.When = c.Committer.When.Format(time.DateOnly)
			h = c.Hash
		}
		if !defHash.IsZero() {
			d.Ahead = countAhead(gr, defHash, h)
			d.Behind = countAhead(gr, h, defHash)
		}
		if gtr != nil {
			if rule, _ := gtr.RuleFor(ctx, r.Name); rule != nil {
				d.RuleName = rule.Name
			}
			if err := gtr.VerifyRef(ctx, r.Name); err == nil {
				d.Verified = true
			} else {
				d.VerifyErr = err.Error()
			}
		}
		rows = append(rows, d)
	}
	return rows
}

// commitFor dereferences annotated tags to their target commit.
func commitFor(gr *git.Repository, h plumbing.Hash) (*object.Commit, error) {
	if c, err := gr.CommitObject(h); err == nil {
		return c, nil
	}
	t, err := gr.TagObject(h)
	if err != nil {
		return nil, err
	}
	return t.Commit()
}

func defaultBranch(gr *git.Repository) string {
	if head, err := gr.Reference(plumbing.HEAD, false); err == nil && head.Type() == plumbing.SymbolicReference {
		return head.Target().String()
	}
	return plumbing.Main.String()
}

// refGroups partitions a flat ref list for the dropdown.
type refGroups struct{ Heads, Tags, Gittuf []refRow }

func groupRefs(refs []refRow) refGroups {
	var g refGroups
	for _, r := range refs {
		switch {
		case strings.HasPrefix(r.Name, "refs/heads/"):
			g.Heads = append(g.Heads, r)
		case strings.HasPrefix(r.Name, "refs/tags/"):
			g.Tags = append(g.Tags, r)
		case gt.IsGittufRef(r.Name):
			g.Gittuf = append(g.Gittuf, r)
		}
	}
	return g
}
