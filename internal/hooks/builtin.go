// Package hooks provides the built-in receive.Hooks that gates pushes on
// gittuf policy and witnesses accepted ref updates.
package hooks

import (
	"context"
	"encoding/json"
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
	"github.com/git-pkgs/silo/internal/store"
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
// (in PostReceive) witnesses accepted updates and enqueues per-branch
// pkgs-reindex jobs.
type Builtin struct {
	BaseURL string
	Signer  signer.Signer

	// Store, when non-nil, is used by PostReceive to enqueue pkgs-reindex
	// jobs for branch updates. Nudge, when non-nil, is called after a
	// successful enqueue so the worker reacts immediately.
	Store *store.Store
	Nudge func()

	lock   *os.File
	gtr    *gt.Repo
	rslTip string
}

const pkgsReindexKind = "pkgs-reindex"

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
	// Verification passed. Roll back so go-git's updateReferences (which
	// rejects Create on an existing ref) applies cleanly. The flock is held
	// through PostReceive so no other reader sees the gap.
	rollback(repo, updates)
	return nil
}

// PostReceive appends a forge-signed witness annotation to the RSL entry the
// client pushed, recording who silo authenticated the push as. After
// witnessing, it enqueues a pkgs-reindex job for each branch update so the
// background worker rebuilds pkgs.sqlite3 against the new tip. The lock
// taken in PreReceive is always released, including on early return.
func (b *Builtin) PostReceive(ctx context.Context, _ *git.Repository, updates []receive.RefUpdate) {
	defer b.releaseLock()

	_, refUpdates := partition(updates)

	if b.gtr != nil && b.rslTip != "" && b.Signer != nil && len(refUpdates) > 0 {
		if keyPEM, err := b.Signer.KeyBytes(); err == nil {
			pusher, _ := receive.PusherFrom(ctx)
			msg := fmt.Sprintf("silo: pushed by %s via %s", pusher.User, pusher.KeyFingerprint)
			if err := b.gtr.Witness(ctx, b.rslTip, msg, keyPEM); err != nil {
				slog.Warn("witness: annotation failed", "err", err)
			}
		} else {
			slog.Warn("witness: signer key not exportable", "err", err)
		}
	}

	b.enqueueReindex(ctx, refUpdates)
}

func (b *Builtin) enqueueReindex(ctx context.Context, refUpdates []receive.RefUpdate) {
	if b.Store == nil {
		return
	}
	repoPath, ok := receive.RepoPathFrom(ctx)
	if !ok {
		return
	}
	owner, repo, ok := splitOwnerRepo(repoPath)
	if !ok {
		return
	}
	r, err := b.Store.RepoByPath(owner, repo)
	if err != nil {
		slog.Warn("pkgs: lookup repo for reindex", "owner", owner, "repo", repo, "err", err)
		return
	}

	var enqueued bool
	for _, u := range refUpdates {
		if !u.Name.IsBranch() {
			continue
		}
		payload := map[string]string{
			"owner":  owner,
			"repo":   repo,
			"branch": u.Name.Short(),
			"old":    u.Old.String(),
			"new":    u.New.String(),
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			slog.Warn("pkgs: marshal reindex payload", "err", err)
			continue
		}
		if _, err := b.Store.EnqueueJob(r.ID, pkgsReindexKind, string(buf)); err != nil {
			slog.Warn("pkgs: enqueue reindex", "err", err)
			continue
		}
		enqueued = true
	}
	if enqueued && b.Nudge != nil {
		b.Nudge()
	}
}

// splitOwnerRepo extracts "alice", "demo" from "<root>/repos/alice/demo.git".
// Returns ok=false when the suffix or shape is unexpected.
func splitOwnerRepo(bareRepoPath string) (owner, repo string, ok bool) {
	name := filepath.Base(bareRepoPath)
	if !filepath.IsAbs(bareRepoPath) && !filepath.IsLocal(bareRepoPath) {
		return "", "", false
	}
	if !hasSuffix(name, ".git") {
		return "", "", false
	}
	owner = filepath.Base(filepath.Dir(bareRepoPath))
	return owner, name[:len(name)-len(".git")], true
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
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
