package gittuf

import (
	"bufio"
	"context"
	"encoding/pem"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"golang.org/x/crypto/ssh"
)

// RSLEntry is one commit on refs/gittuf/reference-state-log, decoded enough
// for the web UI's RSL viewer.
type RSLEntry struct {
	ID          string
	Kind        string // "reference", "annotation", or "propagation"
	Number      string
	Ref         string
	TargetID    string
	Message     string // decoded annotation message, if any
	AnnotatesID string // first entryID an annotation references, if any
	SignerKeyID string // SSH SHA256 fingerprint of the commit signer, if parseable
	Timestamp   time.Time
}

// SignerFingerprint returns the SSH SHA256 fingerprint of the public key
// embedded in an SSH SIGNATURE armoured block, or "" if absent or unparseable.
func SignerFingerprint(sig string) string { return signerFingerprint(sig) }

// WalkRSL returns RSL entries newest-first by walking the reference-state-log
// commit chain. It reads the repo directly via go-git rather than gittuf's
// internal/rsl package, so it works without shelling out and only needs the
// commit messages, which have a stable line-based format.
func WalkRSL(ctx context.Context, repo *git.Repository) ([]RSLEntry, error) {
	ref, err := repo.Reference(plumbing.ReferenceName(RSLRef), true)
	if err != nil {
		return nil, nil
	}
	var entries []RSLEntry
	h := ref.Hash()
	for !h.IsZero() {
		if err := ctx.Err(); err != nil {
			return entries, err
		}
		c, err := repo.CommitObject(h)
		if err != nil {
			return entries, err
		}
		entries = append(entries, parseRSLCommit(c))
		if len(c.ParentHashes) == 0 {
			break
		}
		h = c.ParentHashes[0]
	}
	return entries, nil
}

func parseRSLCommit(c *object.Commit) RSLEntry {
	e := RSLEntry{
		ID:          c.Hash.String(),
		Timestamp:   c.Committer.When,
		SignerKeyID: signerFingerprint(c.Signature),
	}
	var msgPEM strings.Builder
	inPEM := false
	sc := bufio.NewScanner(strings.NewReader(c.Message))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "RSL Reference Entry":
			e.Kind = "reference"
		case line == "RSL Annotation Entry":
			e.Kind = "annotation"
		case line == "RSL Propagation Entry":
			e.Kind = "propagation"
		case strings.HasPrefix(line, "ref: "):
			e.Ref = strings.TrimPrefix(line, "ref: ")
		case strings.HasPrefix(line, "targetID: "):
			e.TargetID = strings.TrimPrefix(line, "targetID: ")
		case strings.HasPrefix(line, "number: "):
			e.Number = strings.TrimPrefix(line, "number: ")
		case strings.HasPrefix(line, "entryID: ") && e.AnnotatesID == "":
			e.AnnotatesID = strings.TrimPrefix(line, "entryID: ")
		case strings.HasPrefix(line, "-----BEGIN"):
			inPEM = true
			msgPEM.WriteString(line + "\n")
		case inPEM:
			msgPEM.WriteString(line + "\n")
			if strings.HasPrefix(line, "-----END") {
				inPEM = false
			}
		}
	}
	if msgPEM.Len() > 0 {
		if block, _ := pem.Decode([]byte(msgPEM.String())); block != nil {
			e.Message = string(block.Bytes)
		}
	}
	return e
}

func signerFingerprint(sig string) string {
	if sig == "" {
		return ""
	}
	block, _ := pem.Decode([]byte(sig))
	if block == nil || block.Type != "SSH SIGNATURE" {
		return ""
	}
	var sshSig struct {
		Magic     [6]byte
		Version   uint32
		PublicKey []byte
		Rest      []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(block.Bytes, &sshSig); err != nil {
		return ""
	}
	pk, err := ssh.ParsePublicKey(sshSig.PublicKey)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(pk)
}

// PolicySummary is the rendered form of a repo's gittuf policy for the web UI.
type PolicySummary struct {
	Rules      []Rule
	Principals map[string][]string // principal ID -> key IDs
}

// Policy returns the rules and principals declared under refs/gittuf/policy.
func (r *Repo) Policy(ctx context.Context) (*PolicySummary, error) {
	rules, err := r.r.ListRules(ctx, PolicyRef)
	if err != nil {
		return nil, err
	}
	ps := &PolicySummary{Principals: map[string][]string{}}
	for _, dw := range rules {
		d := dw.Delegation
		ids := d.GetPrincipalIDs()
		ps.Rules = append(ps.Rules, Rule{
			Name:       d.ID(),
			Threshold:  d.GetThreshold(),
			Principals: ids.Contents(),
			Patterns:   d.GetProtectedNamespaces(),
		})
	}
	pmap, err := r.r.ListPrincipals(ctx, PolicyRef, "targets")
	if err == nil {
		for id, p := range pmap {
			for _, k := range p.Keys() {
				ps.Principals[id] = append(ps.Principals[id], k.KeyID)
			}
		}
	}
	return ps, nil
}
