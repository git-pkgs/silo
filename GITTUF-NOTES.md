# gittuf integration notes

Findings from embedding `github.com/gittuf/gittuf/experimental/gittuf` server-side, against bare repositories, at commit `6f382ee`. Each entry is self-contained and can be filed as an upstream issue independently.

silo implements [GAP-2 "gittuf on the Forge"](https://github.com/gittuf/gittuf/blob/main/docs/gaps/2/README.md) Configuration A: clients create RSL entries, the forge runs pre-receive verification, users push directly. GAP-2 is currently marked "Implemented: No / Prototype Implementation: None yet"; the entries below are what surfaces when you build one.

Existing-issue cross-reference (searched 2026-06-16 across 223 issues, ~300 PRs): the first three bugs have no prior issue or PR. The signer and verdict items have nothing direct (#902 added the config-load path the signer item wants to bypass; #1315 touches `VerifyMergeable`'s counting, not the result shape). The rotation item overlaps with open #1297 (TAP-8 self-rotation), which addresses the same friction from a different direction.

---

## `GetGoGitRepository` cannot open bare repositories

**What happens.** `verify-ref` (and anything else that reaches `verifyCommitSignature`) fails on a bare repository with `verifying Git namespace policies failed`. The same repository, cloned to a working tree, verifies fine.

**Why.** `pkg/gitinterface/repository.go:38-40`:

```go
func (r *Repository) GetGoGitRepository() (*git.Repository, error) {
    return git.PlainOpenWithOptions(r.gitDirPath, &git.PlainOpenOptions{DetectDotGit: true})
}
```

`r.gitDirPath` is already the resolved git directory (set from `git rev-parse --git-dir` in `LoadRepository`). `DetectDotGit: true` tells go-git to search for a `.git` entry inside that path. A bare repository has no `.git` entry — the directory is the git dir — so go-git returns `ErrRepositoryNotExists`. Every caller of `GetGoGitRepository` then fails: commit signature verification at `commit.go:163`, tag verification at `tag.go`, and so on.

It works on working trees by accident: there `gitDirPath` ends in `/.git`, and go-git's detect logic also accepts being handed a `.git` directory directly.

**To reproduce.**

```sh
git init --bare /tmp/bare.git
cd /tmp/bare.git
# set up policy + RSL by pushing from a working clone, then:
gittuf verify-ref refs/heads/main   # fails
# clone the same repo to a working tree and run verify-ref there: passes
```

Or directly in Go:

```go
git.PlainOpenWithOptions("/tmp/bare.git", &git.PlainOpenOptions{DetectDotGit: true})
// returns: repository does not exist
git.PlainOpenWithOptions("/tmp/bare.git", &git.PlainOpenOptions{DetectDotGit: false})
// returns: nil error, repo opens
```

**Suggested fix.** Change `DetectDotGit: true` to `false`. The path is always already the git dir, so detection is never needed; `false` opens both layouts.

**Workaround we're using.** Write a file named `.git` containing `gitdir: .` inside each bare repo. go-git's gitfile resolver follows it back to the bare dir and detection succeeds. System git ignores it.

---

## `LoadRepository` is not safe to call concurrently

**What happens.** Two goroutines calling `LoadRepository` for different paths can each end up with the wrong `gitDirPath`.

**Why.** `pkg/gitinterface/repository.go:72-75` does `os.Chdir(repositoryPath)`, runs `git rev-parse --git-dir`, then `os.Chdir` back. `os.Chdir` is process-wide. The same pattern appears at `utils.go:58`, `status.go:107`, and `tree.go:310`. A server handling pushes to multiple repos at once will hit this.

**Suggested fix.** Pass the directory to git instead of changing into it: `git -C <repositoryPath> rev-parse --git-dir`, or set `cmd.Dir = repositoryPath` on the `exec.Cmd`. Neither needs `os.Chdir`.

**Workaround we're using.** A package-level mutex around every `LoadRepository` call.

---

## `IsBare` returns false for bare repositories named `*.git`

**What happens.** `IsBare()` reports a bare repository at `/data/repos/alice/demo.git` as non-bare.

**Why.** `pkg/gitinterface/repository.go:48-52`:

```go
func (r *Repository) IsBare() bool {
    return !strings.HasSuffix(r.gitDirPath, ".git")
}
```

For a bare repo, `gitDirPath` is the bare directory itself, which by convention ends in `.git`, so this returns false. The check is inverted from what the convention implies.

**Suggested fix.** Ask git: `git rev-parse --is-bare-repository`. Or check for the absence of a worktree rather than the suffix.

---

## No way to pass a signer to `RecordRSLEntryForReference`

**What happens.** A server that wants to sign RSL entries with its own key has to write that key's path into each bare repo's `user.signingKey` git config first.

**Why.** `experimental/gittuf/rsl.go:39-45` takes `signCommit bool`; when true it calls `r.r.CanSign()` which reads `user.signingKey` and `gpg.format` from git config. There's no parameter or option for a `dsse.SignerVerifier`. Every other policy-mutating function in `experimental/gittuf` (`InitializeRoot`, `AddDelegation`, …) does take an explicit signer.

**Suggested fix.** Add a `rslopts.WithSigner(s dsse.SignerVerifier)` record option; when present, use it instead of loading from git config.

---

## `VerifyRef` doesn't say which rule failed

**What happens.** When verification fails, the caller gets `verifying Git namespace policies failed, gittuf policy verification failed` and nothing else. To tell the user *which* rule and *who* could have satisfied it, the caller has to call `ListRules` separately and re-derive the answer.

**Why.** `experimental/gittuf/verify.go:28` returns only `error`. The verifier internally knows which delegation it was checking and which principals it tried.

**Suggested fix.** Return a result struct alongside the error — rule name, threshold, principal IDs, how many signatures were found — so a server can produce `rule 'protect-main' requires 2 of: alice, bob` without a second policy walk.

---

## `gitinterface` requires the `git` binary

**What happens.** `LoadRepository` fails with `unable to find Git binary` if `git` isn't on PATH. Almost every operation in `pkg/gitinterface` (`blob.go`, `commit.go`, `log.go`, `changes.go`, `tree.go`) shells out to it.

**Why.** The package appears to have started on go-git and migrated to shelling out as it needed operations go-git lacked at the time (#145 unvendored a patched go-git once upstream caught up; `GetGoGitRepository()` is a remnant used only for signature verification). So this is accretion rather than a stated position, but the effect is the same: anything embedding gittuf needs `git` on PATH, and the one place that still uses go-git is where the bare-repo bug above lives.

**Suggested change (larger).** A go-git backend for `gitinterface`. `GetGoGitRepository()` already exists; routing the read operations (`cat-file`, `rev-parse`, `for-each-ref`, `diff-tree`) through it would let embedders choose between shelling out and pure-Go without changing the public surface. Most of those operations have direct go-git equivalents now.

---

## No way to reference a principal's key indirectly

Related: open issue [#1297](https://github.com/gittuf/gittuf/issues/1297) discusses TAP-8 self-rotation, where a person updates their own keys using their other keys at threshold. That helps a person rotating within one repo; the case here is one principal (the forge) appearing in many repos.

**What happens.** A forge that adds its witness key to a thousand repos' policies needs a thousand owners to re-sign root metadata when that key rotates.

**Why.** `internal/tuf/tuf.go:84` `Principal.Keys()` returns key material inline; v02 `Person.PublicKeys` (`internal/tuf/v02/tuf.go:33-38`) stores it in the metadata by value.

**Suggested change (larger).** A principal type whose keys are resolved from a separately-signed metadata blob (referenced by URL or git ref). The forge could then rotate its key by updating one signed document under its own authority, and verifiers that want to pin the indirection still can.
