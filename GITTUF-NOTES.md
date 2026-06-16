# gittuf integration notes

Findings from embedding `github.com/gittuf/gittuf/experimental/gittuf` server-side, against bare repositories, at commit `6f382ee`. Each entry is self-contained and can be filed as an upstream issue independently.

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

**Why.** This is a design choice, not a bug. But it means anything embedding gittuf can't ship as a single static binary, and the one place that *does* use go-git instead (`GetGoGitRepository`, for signature verification) is exactly where the bare-repo bug above lives.

**Suggested change (larger).** A go-git backend for `gitinterface`. `GetGoGitRepository()` already exists; routing the read operations through it would let embedders choose between shelling out and pure-Go.

---

## No way to reference a principal's key indirectly

**What happens.** A forge that adds its witness key to a thousand repos' policies needs a thousand owners to re-sign root metadata when that key rotates.

**Why.** `internal/tuf/tuf.go:84` `Principal.Keys()` returns key material inline; v02 `Person.PublicKeys` (`internal/tuf/v02/tuf.go:33-38`) stores it in the metadata by value.

**Suggested change (larger).** A principal type whose keys are resolved from a separately-signed metadata blob (referenced by URL or git ref). The forge could then rotate its key by updating one signed document under its own authority, and verifiers that want to pin the indirection still can.
