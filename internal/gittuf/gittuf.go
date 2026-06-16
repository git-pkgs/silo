// Package gittuf wraps github.com/gittuf/gittuf/experimental/gittuf for use in
// silo's receive path. It serialises LoadRepository calls (gittuf's
// gitinterface uses os.Chdir) and exposes the small surface the hooks need.
package gittuf

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	expgittuf "github.com/gittuf/gittuf/experimental/gittuf"
)

// Ref names under refs/gittuf/ that gittuf manages.
const (
	PolicyRef        = "refs/gittuf/policy"
	PolicyStagingRef = "refs/gittuf/policy-staging"
	RSLRef           = "refs/gittuf/reference-state-log"
	AttestationsRef  = "refs/gittuf/attestations"
	RefPrefix        = "refs/gittuf/"
)

// IsGittufRef reports whether name is under refs/gittuf/.
func IsGittufRef(name string) bool { return strings.HasPrefix(name, RefPrefix) }

// loadMu serialises expgittuf.LoadRepository, which calls os.Chdir.
// See GITTUF-NOTES.md "LoadRepository changes process working directory".
var loadMu sync.Mutex

// Repo wraps an experimental/gittuf Repository for one bare repo.
type Repo struct {
	r    *expgittuf.Repository
	path string
}

// Open loads the gittuf state for the bare repository at repoPath.
func Open(repoPath string) (*Repo, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	r, err := expgittuf.LoadRepository(repoPath)
	if err != nil {
		return nil, fmt.Errorf("gittuf: load %s: %w", repoPath, err)
	}
	return &Repo{r: r, path: repoPath}, nil
}

// HasSignedRoot reports whether refs/gittuf/policy exists and is loadable.
func (r *Repo) HasSignedRoot() bool {
	ok, err := r.r.HasPolicy()
	return err == nil && ok
}

// VerifyRef checks that the current tip of refName is backed by a valid RSL
// chain under the current policy. The caller must have already applied the
// proposed ref updates (including any pushed RSL entries) before calling.
func (r *Repo) VerifyRef(ctx context.Context, refName string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("gittuf: verify panicked: %v", rec)
		}
	}()
	return r.r.VerifyRef(ctx, refName)
}

// Rule describes the policy rule governing a ref pattern.
type Rule struct {
	Name       string
	Threshold  int
	Principals []string
}

// RuleFor returns the first delegation rule whose patterns match refName.
// Returns nil if no rule matches (the ref is unprotected).
func (r *Repo) RuleFor(ctx context.Context, refName string) (*Rule, error) {
	rules, err := r.r.ListRules(ctx, PolicyRef)
	if err != nil {
		return nil, err
	}
	target := "git:" + refName
	for _, dw := range rules {
		d := dw.Delegation
		for _, pat := range d.GetProtectedNamespaces() {
			if matchPattern(pat, target) {
				ids := d.GetPrincipalIDs()
				return &Rule{
					Name:       d.ID(),
					Threshold:  d.GetThreshold(),
					Principals: ids.Contents(),
				}, nil
			}
		}
	}
	return nil, nil
}

// matchPattern does fnmatch-style matching for gittuf rule patterns.
func matchPattern(pattern, name string) bool {
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == name
}
