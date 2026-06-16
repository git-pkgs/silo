# gittuf integration notes

Findings from embedding `github.com/gittuf/gittuf/experimental/gittuf` server-side, against bare repositories, at commit `6f382ee`. silo implements [GAP-2 "gittuf on the Forge"](https://github.com/gittuf/gittuf/blob/main/docs/gaps/2/README.md) Configuration A; that GAP is currently marked "Prototype Implementation: None yet".

Four items. The first is a one-line fix. The second subsumes what were three separate bugs by replacing the layer they live in. The last two are API shape, independent of the git layer.

---

## `GetGoGitRepository` cannot open bare repositories

`pkg/gitinterface/repository.go:39` opens with `git.PlainOpenWithOptions(r.gitDirPath, &git.PlainOpenOptions{DetectDotGit: true})`. `gitDirPath` is already the resolved git dir; `DetectDotGit: true` makes go-git look for a `.git` entry inside it, which a bare repo doesn't have, so it returns `ErrRepositoryNotExists`. Every caller (`verifyCommitSignature`, tag verification) then fails on bare repos with the generic `verifying Git namespace policies failed`.

**Fix:** change `true` to `false`. The path is always already the git dir; `false` opens both layouts. Applied on `git-pkgs/gittuf@silo`.

---

## A go-git read backend for `gitinterface` removes the server-side blockers

Three separate problems all come from `pkg/gitinterface` shelling to the `git` binary:

- `LoadRepository` does `os.Chdir(repositoryPath)` (`repository.go:72-75`, also `utils.go:58`, `status.go:107`, `tree.go:310`) to run `git rev-parse --git-dir`. `os.Chdir` is process-wide; concurrent calls for different repos race.
- `IsBare()` (`repository.go:48-52`) returns `!strings.HasSuffix(gitDirPath, ".git")`, which is backwards for the `name.git` bare-repo convention.
- `LoadRepository` does `exec.LookPath("git")` and fails without it; embedders can't ship a single binary.

go-git on main covers every read operation `gitinterface` shells out for except `merge-tree` — see the inventory table in `GOGIT-NOTES.md`. A go-git backend for the read path (`cat-file`, `ls-tree`, `diff-tree`, `rev-parse`, `update-ref`, `merge-base`, `config`, `for-each-ref`, `log`) holds one `*git.Repository` opened with `DetectDotGit: false`, needs no `os.Chdir`, knows bare-ness from `Config().Core.IsBare`, and needs no binary. The worktree operations (`status`, `restore`) and `merge-tree` can keep shelling out; a forge doesn't reach them.

`GetGoGitRepository()` already exists; routing the reads through it is the change.

The `os.Chdir` race specifically is fixed on `git-pkgs/gittuf@silo` by adding `executor.withDir` (sets `cmd.Dir`) and switching `LoadRepository` to `rev-parse --absolute-git-dir`; the binary requirement and `IsBare` remain until the broader backend change.

---

## No way to pass a signer to `RecordRSLEntryForReference`

`experimental/gittuf/rsl.go:39-45` takes `signCommit bool` and reads `user.signingKey`/`gpg.format` from the repo's git config via `r.r.CanSign()`. There is no parameter or option for a `dsse.SignerVerifier`, unlike every other policy-mutating function (`InitializeRoot`, `AddDelegation`, …). A forge wanting to witness with its own key has to write that key's path into each bare repo's git config.

**Fix:** a record/annotate option carrying the signer; when present, skip `CanSign()` and use the existing `CommitUsingSpecificKey` path. Applied on `git-pkgs/gittuf@silo` as `WithRecordSigningKeyBytes` / `WithAnnotateSigningKeyBytes` taking PEM bytes (matching what `CommitUsingSpecificKey` already accepts); a `dsse.SignerVerifier` variant would be cleaner long-term but needs the commit-signing path to accept an interface instead of raw key material.

---

## `VerifyRef` doesn't say which rule failed

`experimental/gittuf/verify.go:28` returns only `error` with a generic message. The verifier internally knows which delegation it was checking and which principals it tried; a caller wanting to tell the user `rule 'protect-main' requires 2 of: alice, bob` has to call `ListRules` and re-derive it.

**Fix:** return `(Verdict{Rule, Threshold, Principals, Approvals}, error)` or equivalent.

---

## `TestCanSign` leaks the host's global git config

`pkg/gitinterface/signature_test.go` `TestCanSign/explicit_ssh,_no_key` sets `gpg.format=ssh` in a temp repo and expects `CanSign()` to fail because no `user.signingKey` is set. On a host with `user.signingKey` in `~/.gitconfig`, the temp repo inherits it via scoped config and the test passes `CanSign()` unexpectedly. The test should set `GIT_CONFIG_GLOBAL=/dev/null` (or unset the key in the temp repo) to isolate. Pre-existing; surfaced while running the fork's suite.

---

## Related upstream

Key rotation across many repos (a forge key in N policies needing N owner re-signs) overlaps open [#1297](https://github.com/gittuf/gittuf/issues/1297) on TAP-8 self-rotation; same friction, different angle.
