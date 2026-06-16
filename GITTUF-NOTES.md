# gittuf integration notes

Observations from embedding `github.com/gittuf/gittuf/experimental/gittuf` server-side in silo's receive path. Each entry should make sense to a gittuf maintainer who hasn't read silo's code. See `SPEC.md` § "gittuf feedback" for when to add an entry.

Format per entry:

```
## <short title>
kind: friction | missing | bug | opportunity | question
gittuf: <file:line or func>
silo:   <file:line where it bit>
<what happened, what was expected, what was done instead>
```

---

## gitinterface shells out to system git
kind: friction
gittuf: pkg/gitinterface/repository.go:58, blob.go:14, commit.go:50, log.go:22, changes.go:28
silo:   internal/gittuf (design assumption)
Every operation in `pkg/gitinterface` calls the `git` binary via `exec.Cmd`. `LoadRepository` does `exec.LookPath("git")` and fails if absent. A forge embedding gittuf cannot ship as a single static binary without also bundling git. A go-git backend for `gitinterface` (it already has `GetGoGitRepository()` at repository.go:38, but nothing uses it for the operations) would let embedders choose.

## LoadRepository changes process working directory
kind: bug
gittuf: pkg/gitinterface/repository.go:72-75
silo:   internal/gittuf (concurrent receive-pack)
`LoadRepository` does `os.Chdir(repositoryPath)` to run `git rev-parse --git-dir`, then `os.Chdir` back. `os.Chdir` is process-global; two goroutines calling `LoadRepository` for different repos race and one will resolve the wrong gitDir. The same pattern appears at utils.go:58, status.go:107, tree.go:310. A server handling concurrent pushes hits this. The `rev-parse` call could pass `-C repositoryPath` instead of chdir; the others could set `cmd.Dir`.

## IsBare returns false for bare repos ending in .git
kind: bug
gittuf: pkg/gitinterface/repository.go:48-52
silo:   internal/gittuf
`IsBare()` returns `!strings.HasSuffix(r.gitDirPath, ".git")`. For a bare repo at `/data/repos/owner/name.git`, `rev-parse --git-dir` returns that path, which ends in `.git`, so `IsBare()` returns false. The convention for bare repos is exactly to name the directory `<name>.git`. `git rev-parse --is-bare-repository` would give the right answer.

## GetGoGitRepository fails on bare repositories

kind: bug
gittuf: pkg/gitinterface/repository.go:38-40
silo:   internal/gitstore/gitstore.go (workaround), internal/hooks/builtin.go (caller)

`GetGoGitRepository()` opens with `git.PlainOpenWithOptions(r.gitDirPath, &git.PlainOpenOptions{DetectDotGit: true})`. `r.gitDirPath` is already the resolved git dir (set via `git rev-parse --git-dir`). With `DetectDotGit: true`, go-git's `dotGitToOSFilesystems` looks for a `.git` entry inside that path and walks up if absent; a bare repo has no `.git` entry, so it returns `ErrRepositoryNotExists`. Every caller of `GetGoGitRepository` then fails on bare repos: `verifyCommitSignature` (commit.go:163), tag verification, etc. The error surfaces as the generic `verifying Git namespace policies failed`.

Reproduction: `git init --bare /tmp/b && cd /tmp/b && go run -mod=mod github.com/gittuf/gittuf verify-ref refs/heads/x` against any bare repo with policy/RSL fails at signature verification; the same repo cloned to a working tree verifies.

Upstream fix (one line): change `DetectDotGit: true` to `false` at repository.go:39. The path is already the git dir, so detection is never needed and `false` opens both bare and working layouts.

silo workaround: `gitstore.Init` writes `.git` containing `gitdir: .` inside each bare repo. go-git's gitfile resolver then dereferences it back to the bare dir itself, and `DetectDotGit: true` succeeds. System git ignores a `.git` gitfile inside what it already knows is a bare repo, so nothing else changes. Remove the workaround once the upstream fix lands.

## RecordRSLEntryForReference takes signing config from git, not a Signer

kind: friction
gittuf: experimental/gittuf/rsl.go:39-45, pkg/gitinterface/repository.go (CanSign)
silo:   internal/hooks/builtin.go PostReceive

`RecordRSLEntryForReference(ctx, ref, signCommit, opts)` checks `r.r.CanSign()` which reads `user.signingKey`/`gpg.format` from the repo's git config. There is no way to pass a `dsse.SignerVerifier` directly. A forge wanting to witness with its own key must write that key's path into each bare repo's git config, which couples on-disk state to the forge process and conflicts if two silo instances share storage. silo's `Witness` is a no-op pending either an upstream `WithSigner(s)` RecordOption or a per-repo config-write shim; the witness role is not policy-required so verification still passes without it.

## VerifyRef returns only error, not which rule failed

kind: opportunity
gittuf: experimental/gittuf/verify.go:28
silo:   internal/hooks/builtin.go buildRejection

`VerifyRef` returns `error` with a generic message. To produce a useful rejection (`rule 'protect-main' requires 1 of: alice`) silo calls `ListRules` separately and pattern-matches the failing ref. A `VerifyRef` that returned `(Verdict{Rule, Threshold, Principals, Approvals}, error)` would let any forge build its rejection message without re-deriving policy state.

## Principal keys are embedded by value
kind: opportunity
gittuf: internal/tuf/tuf.go:84, internal/tuf/v02/tuf.go:33-38
silo:   forge key rotation
`Principal.Keys()` returns key material inline; v02 `Person.PublicKeys` is a map of keys stored in the metadata. A forge whose witness key appears in N repos' policies needs N root re-signs to rotate. A principal type whose `Keys()` resolves from a separately-signed metadata blob (referenced by URL or ref) would let a forge rotate its own key under its own signature without touching repo roots, while still letting verifiers pin the indirection if they want.
