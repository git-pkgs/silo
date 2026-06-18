package web

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	gt "github.com/git-pkgs/silo/internal/gittuf"
	"github.com/git-pkgs/silo/internal/pkgs"
)

type compareView struct {
	Base, Head                          string
	BaseHash, HeadHash                  string
	Ahead, Files                        int
	Diff                                template.HTML
	Rule                                *gt.Rule
	Mergeable, NeedRSL                  bool
	MergeErr, MergeSteps                string
	Deps                                []*pkgs.FileDelta
	DepsAdded, DepsRemoved, DepsUpdated int
}

func (h *handler) compare(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	spec := r.PathValue("spec")
	base, head, ok := strings.Cut(spec, "...")
	if !ok || base == "" || head == "" {
		http.Error(w, "compare expects base...head", http.StatusBadRequest)
		return
	}
	bc, hc, err := resolvePair(gr, base, head)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := compareView{
		Base: base, Head: head,
		BaseHash: bc.Hash.String(), HeadHash: hc.Hash.String(),
	}
	bt, _ := bc.Tree()
	ht, _ := hc.Tree()
	if changes, err := object.DiffTree(bt, ht); err == nil {
		v.Files = len(changes)
		if patch, err := changes.Patch(); err == nil {
			if s := patch.String(); s != "" {
				v.Diff = renderDiff(s)
			}
		}
	}
	v.Deps = manifestDeltas(bt, ht, h.deltaCache)
	v.DepsAdded, v.DepsRemoved, v.DepsUpdated = commitDeltaSummary(v.Deps)
	v.Ahead = countAhead(gr, bc.Hash, hc.Hash)

	if gtr, err := gt.Open(fsPath); err == nil {
		v.Rule, _ = gtr.RuleFor(r.Context(), qualifyRef(gr, base))
		needRSL, merr := gtr.VerifyMergeable(r.Context(), base, head)
		if merr == nil {
			v.Mergeable, v.NeedRSL = true, needRSL
		} else {
			v.MergeErr = merr.Error()
		}
	}
	v.MergeSteps = mergeSteps(h.baseURL, repoPath, base, head)

	h.render(w, r, "compare", struct {
		page
		compareView
	}{h.page(r, gr, repoPath, fsPath, "commits/branches", head), v})
}

func resolvePair(gr *git.Repository, a, b string) (*object.Commit, *object.Commit, error) {
	ah, err := gr.ResolveRevision(plumbing.Revision(a))
	if err != nil {
		return nil, nil, err
	}
	bh, err := gr.ResolveRevision(plumbing.Revision(b))
	if err != nil {
		return nil, nil, err
	}
	ac, err := gr.CommitObject(*ah)
	if err != nil {
		return nil, nil, err
	}
	bc, err := gr.CommitObject(*bh)
	if err != nil {
		return nil, nil, err
	}
	return ac, bc, nil
}

const aheadCap = 1000

func countAhead(gr *git.Repository, base, head plumbing.Hash) int {
	iter, err := gr.Log(&git.LogOptions{From: head})
	if err != nil {
		return 0
	}
	n := 0
	_ = iter.ForEach(func(c *object.Commit) error {
		if c.Hash == base || n >= aheadCap {
			return errStop
		}
		n++
		return nil
	})
	return n
}

// qualifyRef expands a short branch name to refs/heads/<name> when such a ref
// exists, so RuleFor's git: pattern matching works.
func qualifyRef(gr *git.Repository, name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	if _, err := gr.Reference(plumbing.NewBranchReferenceName(name), false); err == nil {
		return "refs/heads/" + name
	}
	return name
}

func mergeSteps(baseURL, repo, base, head string) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s + "\n") }
	w("git fetch " + baseURL + "/" + repo + ".git " + head + " '+refs/gittuf/*:refs/gittuf/*'")
	w("git switch " + shortRef(base))
	w("git merge --no-ff FETCH_HEAD")
	w("gittuf rsl record " + shortRef(base) + " --local-only")
	w("git push origin 'refs/gittuf/*:refs/gittuf/*' " + shortRef(base))
	return b.String()
}

func shortRef(r string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(r, p) {
			return strings.TrimPrefix(r, p)
		}
	}
	return r
}
