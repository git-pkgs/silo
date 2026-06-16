// Package hooks provides the built-in receive.Hooks that gates pushes on
// gittuf policy and witnesses accepted ref updates.
package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	gt "github.com/git-pkgs/silo/internal/gittuf"
	"github.com/git-pkgs/silo/internal/receive"
	"github.com/git-pkgs/silo/internal/signer"
)

// Verdict records the policy outcome for one ref update.
type Verdict struct {
	Ref        string
	Rule       string
	Principals []string
	Threshold  int
	Met        bool
}

// Builtin is silo's standard receive hook chain. It takes the per-repo flock,
// gates pushes on the presence of a signed root and on gittuf VerifyRef, and
// (in PostReceive) witnesses accepted updates.
type Builtin struct {
	BaseURL string
	Signer  signer.Signer

	lock   *os.File
	gtr    *gt.Repo
	rslTip string
}

const lockPerm = 0o600

// PreReceive applies the proposed updates under the per-repo flock, then runs
// gittuf verification against the resulting state. On failure all updates are
// rolled back and a RejectionError is returned. On success the updates are
// left applied; receive.ReceivePack's own apply step is then a no-op.
func (b *Builtin) PreReceive(ctx context.Context, repo *git.Repository, updates []receive.RefUpdate) error {
	repoPath, ok := receive.RepoPathFrom(ctx)
	if !ok {
		return fmt.Errorf("hooks: repo path missing from context")
	}

	if err := b.acquireLock(repoPath); err != nil {
		return err
	}

	gittufUpdates, refUpdates := partition(updates)

	gtr, err := gt.Open(repoPath)
	if err != nil {
		b.releaseLock()
		return err
	}

	// Apply gittuf refs first so HasSignedRoot and VerifyRef see the policy and
	// RSL state carried in this push.
	if err := apply(repo, gittufUpdates); err != nil {
		b.releaseLock()
		return err
	}

	if len(refUpdates) > 0 && !gtr.HasSignedRoot() {
		rollback(repo, gittufUpdates)
		b.releaseLock()
		return &receive.RejectionError{
			Ref:    refUpdates[0].Name.String(),
			Reason: "repo not initialised: run `gittuf trust init` and push refs/gittuf/policy",
		}
	}

	if err := apply(repo, refUpdates); err != nil {
		rollback(repo, gittufUpdates)
		b.releaseLock()
		return err
	}

	for _, u := range refUpdates {
		if err := gtr.VerifyRef(ctx, u.Name.String()); err != nil {
			rollback(repo, updates)
			rej := b.buildRejection(ctx, gtr, u, err)
			b.releaseLock()
			return rej
		}
	}

	b.gtr = gtr
	b.rslTip = gtr.RSLTip()
	return nil
}

// PostReceive appends a forge-signed witness annotation to the RSL entry the
// client pushed, recording who silo authenticated the push as, then releases
// the lock taken in PreReceive.
func (b *Builtin) PostReceive(ctx context.Context, _ *git.Repository, updates []receive.RefUpdate) {
	defer b.releaseLock()

	if b.gtr == nil || b.rslTip == "" || b.Signer == nil {
		return
	}
	_, refUpdates := partition(updates)
	if len(refUpdates) == 0 {
		return
	}
	keyPEM, err := b.Signer.KeyBytes()
	if err != nil {
		slog.Warn("witness: signer key not exportable", "err", err)
		return
	}
	pusher, _ := receive.PusherFrom(ctx)
	msg := fmt.Sprintf("silo: pushed by %s via %s", pusher.User, pusher.KeyFingerprint)
	if err := b.gtr.Witness(ctx, b.rslTip, msg, keyPEM); err != nil {
		slog.Warn("witness: annotation failed", "err", err)
	}
}

func (b *Builtin) buildRejection(ctx context.Context, gtr *gt.Repo, u receive.RefUpdate, verifyErr error) *receive.RejectionError {
	rej := &receive.RejectionError{Ref: u.Name.String()}
	if p, ok := receive.PusherFrom(ctx); ok {
		rej.Pusher = p.User
		rej.PusherKey = p.KeyFingerprint
	}
	if rule, err := gtr.RuleFor(ctx, u.Name.String()); err == nil && rule != nil {
		rej.Rule = rule.Name
		rej.Threshold = rule.Threshold
		rej.Principals = rule.Principals
		for _, pr := range rule.Principals {
			if pr == rej.Pusher {
				rej.InSet = true
			}
		}
		if b.BaseURL != "" {
			if path, ok := receive.RepoPathFrom(ctx); ok {
				rej.PolicyURL = b.BaseURL + "/" + ownerRepo(path) + "/policy#" + rule.Name
			}
		}
	} else {
		rej.Reason = "policy: " + verifyErr.Error()
	}
	return rej
}

func ownerRepo(bareRepoPath string) string {
	name := filepath.Base(bareRepoPath)
	owner := filepath.Base(filepath.Dir(bareRepoPath))
	return owner + "/" + name[:len(name)-len(".git")]
}

func partition(updates []receive.RefUpdate) (gittufRefs, otherRefs []receive.RefUpdate) {
	for _, u := range updates {
		if gt.IsGittufRef(u.Name.String()) {
			gittufRefs = append(gittufRefs, u)
		} else {
			otherRefs = append(otherRefs, u)
		}
	}
	return gittufRefs, otherRefs
}

func apply(repo *git.Repository, updates []receive.RefUpdate) error {
	for _, u := range updates {
		if u.IsDelete() {
			if err := repo.Storer.RemoveReference(u.Name); err != nil {
				return err
			}
			continue
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(u.Name, u.New)); err != nil {
			return err
		}
	}
	return nil
}

func rollback(repo *git.Repository, updates []receive.RefUpdate) {
	for _, u := range updates {
		if u.Old.IsZero() {
			_ = repo.Storer.RemoveReference(u.Name)
		} else {
			_ = repo.Storer.SetReference(plumbing.NewHashReference(u.Name, u.Old))
		}
	}
}

func (b *Builtin) acquireLock(repoPath string) error {
	// #nosec G304 -- repoPath is resolved by gitstore from validated owner/name
	f, err := os.OpenFile(filepath.Join(repoPath, "silo.lock"), os.O_CREATE|os.O_RDWR, lockPerm)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return fmt.Errorf("hooks: flock %s: %w", repoPath, err)
	}
	b.lock = f
	return nil
}

func (b *Builtin) releaseLock() {
	if b.lock == nil {
		return
	}
	_ = syscall.Flock(int(b.lock.Fd()), syscall.LOCK_UN)
	_ = b.lock.Close()
	b.lock = nil
}
