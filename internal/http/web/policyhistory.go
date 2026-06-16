package web

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

type policyChange struct {
	Hash, Subject, SignerKeyID, SignerName, Age string
	Files                                       []string
	Diff                                        template.HTML
}

func (h *handler) policyHistory(w http.ResponseWriter, r *http.Request) {
	gr, repoPath, fsPath, ok := h.open(w, r)
	if !ok {
		return
	}
	var names map[string]string
	if gtr, err := gt.Open(fsPath); err == nil {
		ps, _ := gtr.Policy(r.Context())
		names = h.signerNames(ps)
	}
	var changes []policyChange
	ref, err := gr.Reference(plumbing.ReferenceName(gt.PolicyRef), true)
	if err == nil {
		iter, _ := gr.Log(&git.LogOptions{From: ref.Hash()})
		_ = iter.ForEach(func(c *object.Commit) error {
			pc := policyChange{
				Hash:        c.Hash.String(),
				Subject:     firstLine(c.Message),
				SignerKeyID: gt.SignerFingerprint(c.Signature),
				Age:         ago(c.Committer.When),
			}
			pc.SignerName = names[pc.SignerKeyID]
			pc.Files, pc.Diff = metadataDiff(c)
			changes = append(changes, pc)
			return nil
		})
	}
	h.render(w, r, "policy_history", struct {
		page
		Changes []policyChange
	}{h.page(r, gr, repoPath, fsPath, "policy/history", ""), changes})
}

func metadataDiff(c *object.Commit) ([]string, template.HTML) {
	to, err := c.Tree()
	if err != nil {
		return nil, ""
	}
	var from *object.Tree
	if p, err := c.Parent(0); err == nil {
		from, _ = p.Tree()
	}
	changes, err := object.DiffTree(from, to)
	if err != nil {
		return nil, ""
	}
	var files []string
	for _, ch := range changes {
		name := ch.To.Name
		if name == "" {
			name = ch.From.Name
		}
		if strings.HasPrefix(name, "metadata/") {
			files = append(files, name)
		}
	}
	patch, err := changes.Patch()
	if err != nil || patch == nil {
		return files, ""
	}
	s := patch.String()
	if s == "" {
		return files, ""
	}
	return files, renderDiff(s)
}
