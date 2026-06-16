// Package gittuf wraps github.com/gittuf/gittuf/experimental/gittuf for use in
// silo's receive path. It serialises LoadRepository calls (gittuf's
// gitinterface uses os.Chdir) and exposes the small surface the hooks need.
package gittuf

import (
	"context"
	"fmt"
	"path"
	"strings"

	expgittuf "github.com/gittuf/gittuf/experimental/gittuf"
	rslopts "github.com/gittuf/gittuf/experimental/gittuf/options/rsl"
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

// Repo wraps an experimental/gittuf Repository for one bare repo.
type Repo struct {
	r    *expgittuf.Repository
	path string
}

// Open loads the gittuf state for the bare repository at repoPath.
func Open(repoPath string) (*Repo, error) {
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

// VerifyMergeable reports whether featureRef can be merged into targetRef
// under the current policy. needRSL is true when the merger must also append a
// signed RSL entry for the merge to verify.
func (r *Repo) VerifyMergeable(ctx context.Context, targetRef, featureRef string) (needRSL bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("gittuf: verify-mergeable panicked: %v", rec)
		}
	}()
	return r.r.VerifyMergeable(ctx, targetRef, featureRef)
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

// RSLTip returns the current tip of refs/gittuf/reference-state-log, or empty
// if the RSL doesn't exist yet.
func (r *Repo) RSLTip() string {
	h, err := r.r.GetGitRepository().GetReference(RSLRef)
	if err != nil {
		return ""
	}
	return h.String()
}

// Witness appends a forge-signed annotation to the RSL entry at entryID,
// recording who silo authenticated for the push. The forge key signs the
// annotation; it does not authorise the underlying ref movement.
func (r *Repo) Witness(ctx context.Context, entryID, message string, signingKeyPEM []byte) error {
	if entryID == "" {
		return nil
	}
	return r.r.RecordRSLAnnotation(ctx, []string{entryID}, false, message,
		true,
		rslopts.WithAnnotateLocalOnly(),
		rslopts.WithAnnotateSigningKeyBytes(signingKeyPEM),
	)
}

// Hook describes one in-policy Lua hook.
type Hook struct {
	Stage, Name, Environment, BlobID string
	Principals                       []string
	Hashes                           map[string]string
	Timeout                          int
}

// Hooks returns all in-policy hooks across stages.
func (r *Repo) Hooks(ctx context.Context) ([]Hook, error) {
	m, err := r.r.ListHooks(ctx, PolicyRef)
	if err != nil {
		return nil, err
	}
	var out []Hook
	for stage, hs := range m {
		for _, h := range hs {
			ids := h.GetPrincipalIDs()
			out = append(out, Hook{
				Stage:       stage.String(),
				Name:        h.ID(),
				Environment: h.GetEnvironment().String(),
				BlobID:      h.GetBlobID().String(),
				Principals:  ids.Contents(),
				Hashes:      h.GetHashes(),
				Timeout:     h.GetTimeout(),
			})
		}
	}
	return out, nil
}

// Rule describes the policy rule governing a ref pattern.
type Rule struct {
	Name       string
	Threshold  int
	Principals []string
	Patterns   []string
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
