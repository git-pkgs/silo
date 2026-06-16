package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/go-git/go-git/v6"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

// refStateHash returns a stable digest of the repo's ref set so cached
// verify results invalidate exactly when a ref moves.
func refStateHash(gr *git.Repository) string {
	h := sha256.New()
	for _, r := range listRefs(gr) {
		h.Write([]byte(r.Name))
		h.Write([]byte{0})
		h.Write([]byte(r.Hash))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type verifyCacheEntry struct {
	rows   []verifyRow
	bad    int
	policy *gt.PolicySummary
	names  map[string]string
}

type verifyCache struct {
	mu sync.Mutex
	m  map[string]verifyCacheEntry // key: fsPath + "@" + refStateHash
}

func (c *verifyCache) entry(ctx context.Context, h *handler, gr *git.Repository, fsPath string) verifyCacheEntry {
	key := fsPath + "@" + refStateHash(gr)
	c.mu.Lock()
	if e, ok := c.m[key]; ok {
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()

	gtr, _ := gt.Open(fsPath)
	rows, bad := verifyRefs(ctx, gr, gtr)
	var ps *gt.PolicySummary
	if gtr != nil {
		ps, _ = gtr.Policy(ctx)
	}
	e := verifyCacheEntry{rows: rows, bad: bad, policy: ps, names: h.signerNames(ps)}

	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]verifyCacheEntry{}
	}
	for k := range c.m {
		if len(k) > len(fsPath) && k[:len(fsPath)] == fsPath && k[len(fsPath)] == '@' {
			delete(c.m, k)
		}
	}
	c.m[key] = e
	c.mu.Unlock()
	return e
}

func (e verifyCacheEntry) verifyOf(ref string) (ok bool, errMsg string) {
	for _, r := range e.rows {
		if r.Ref == ref {
			return r.Err == "", r.Err
		}
	}
	return false, ""
}
