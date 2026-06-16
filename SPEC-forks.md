# silo spec — gittuf and go-git fork track

Goal: remove every workaround in silo by fixing the upstream cause in a fork, pinned via `replace` in go.mod, with each fix small enough to PR upstream on its own. silo's existing test suite (`go test -race ./...` + the three txtar files) is the integration proof at every step.

Conventions, quality bar, and commit rules are inherited from `SPEC.md`. Findings go in `GITTUF-NOTES.md` / `GOGIT-NOTES.md` only if they're new; don't re-record what's already there.

## Bootstrap

Forks live at `github.com/git-pkgs/gittuf` and `github.com/git-pkgs/go-git`. Local checkouts sit alongside silo:

```sh
git clone git@github.com:git-pkgs/gittuf.git ../gittuf-fork
git -C ../gittuf-fork remote add upstream https://github.com/gittuf/gittuf
git -C ../gittuf-fork checkout 6f382ee5c02943dee0195af6bee751a2d24a6533 -b silo
```

silo's `go.mod`:

```
replace github.com/gittuf/gittuf => ../gittuf-fork
```

go-git fork is cloned at the v6-port milestone. Commits go on the `silo` branch in each fork; don't push without asking.

---

## Milestone: gittuf `DetectDotGit` fix

In `../gittuf-fork`: change `pkg/gitinterface/repository.go:39` `DetectDotGit: true` → `false`. Add a test in that package that opens a `git init --bare` repo via `LoadRepository` and asserts `GetGoGitRepository()` returns no error.

In silo: delete the gitfile-write block from `internal/gitstore/gitstore.go` (and the `gitfilePerm` const). Delete the workaround note from `GITTUF-NOTES.md`.

**Done when:** `go test -race ./...` and all three txtar pass with no `.git` file inside `$SILO_DATA/repos/*/*.git/`. Add an assertion to `03_gittuf.txtar`: `! exists $SILO_DATA/repos/alice/demo.git/.git`.

---

## Milestone: gittuf `os.Chdir` removal

In `../gittuf-fork`: replace each `os.Chdir` in `pkg/gitinterface/` (`repository.go:72-75`, `utils.go:58-61`, `status.go:107-110`, `tree.go:310-313`) with `cmd.Dir = <path>` on the `executor`, or `git -C <path>` in the args. Add a test that calls `LoadRepository` for two different repos concurrently from 10 goroutines each and asserts each gets its own `gitDirPath`.

In silo: delete `loadMu` and its `Lock`/`Unlock` from `internal/gittuf/gittuf.go`. Delete the workaround note.

**Done when:** silo's tests pass; the new gittuf concurrency test passes with `-race`.

---

## Milestone: gittuf `WithSigner` for RSL recording

In `../gittuf-fork`: add `WithSigner(s sslibdsse.SignerVerifier)` to `experimental/gittuf/options/rsl`. In `RecordRSLEntryForReference` (`rsl.go:39`), when set, skip `r.r.CanSign()` and pass the signer through to the commit-creation path instead of reading git config. Same for `RecordRSLAnnotation`.

In silo: implement `internal/gittuf.(*Repo).Witness(ctx, refName, pusher, signer)` calling `RecordRSLEntryForReference(..., rslopts.WithSigner(adapt(signer)), rslopts.WithRecordLocalOnly())` then `RecordRSLAnnotation` with `silo: pushed by <user> via <fp>`. Adapt `internal/signer.Signer` to `sslibdsse.SignerVerifier` (it's `Sign`/`Verify`/`KeyID`/`Public`). Wire into `hooks.Builtin.PostReceive`.

Extend `03_gittuf.txtar`: after alice's push, assert `git -C $SILO_DATA/repos/alice/demo.git log refs/gittuf/reference-state-log` shows a commit with `silo: pushed by alice` in its message.

**Done when:** the txtar assertion passes; `gittuf verify-ref main` in the verify clone still passes (witness annotation doesn't break the chain).

---

## Milestone: port silo to go-git v6

```sh
git clone https://github.com/go-git/go-git ../go-git-fork
git -C ../go-git-fork checkout v6.0.0-alpha.4 -b silo
```

Add `replace github.com/go-git/go-git/v6 => ../go-git-fork` and change silo's imports from `/v5` → `/v6`.

Port `internal/receive` to v6's `packp` API: `ReferenceUpdateRequest` → `UpdateRequests`, `NewAdvRefs()` → zero-value, map → slice for `AdvRefs.References`. Drop silo's `decodeHaves` (v6 has `UploadHaves.Decode`). Port `internal/http/git` and `internal/ssh` onto `backend.Backend.ServeHTTP` / `ServeConn` for upload-pack; keep silo's own `ReceivePack` for now since v6's has no hook yet.

gittuf at `6f382ee` depends on go-git v5. Bump gittuf-fork to v6 as the first commit of this milestone so there is exactly one go-git in the binary; do not let v5 and v6 coexist. The gittuf changes are: import path `/v5` → `/v6`, `packp` type renames where used, and `PlainOpenWithOptions` (already touched in the DetectDotGit fix). gittuf's own test suite is the proof.

**Done when:** all three txtar pass on v6. `internal/http/git/git.go` no longer has `decodeHaves`.

---

## Milestone: go-git v6 receive-pack hook

In `../go-git-fork`: add to `plumbing/transport/request.go`:

```go
type ReceivePackHooks struct {
    PreReceive  func(ctx context.Context, st storage.Storer, cmds []*packp.Command) error
    PostReceive func(ctx context.Context, st storage.Storer, cmds []*packp.Command)
}
```

and a `Hooks *ReceivePackHooks` field on `ReceivePackRequest`. In `transport/receive_pack.go`, after unpack and before `updateReferences`, call `PreReceive`; on error, fill `cmdStatus[ref] = err` for every command and skip the update. After `updateReferences`, call `PostReceive`. Add tests in go-git mirroring silo's `TestReceive_PreReceiveReject`.

In silo: replace `internal/receive.ReceivePack` with a thin wrapper that builds a `ReceivePackRequest{Hooks: ...}` and calls `transport.ReceivePack`. Keep `RejectionError` and the sideband formatting (go-git's hook returns `error`; silo's hook returns `*RejectionError` and the wrapper writes `Sideband()` lines via the muxer before returning the error). `Advertise` becomes `transport.AdvertiseRefs`.

**Done when:** all three txtar pass; `internal/receive/receive.go` is under 80 lines; `internal/receive/unpack.go` is deleted (limits move to a `LimitReader` wrapper passed as `r`).

---

## Upstreaming

Each milestone's fork commit is a standalone patch. Open PRs after silo's suite proves them, in this order: gittuf `DetectDotGit` (one line, easy review), gittuf `os.Chdir` (mechanical), go-git receive hook (references their own TODO and #2185), gittuf `WithSigner`. Don't open any until asked.

## Findings

All five milestones landed. silo builds against `github.com/git-pkgs/{gittuf,go-git}@silo` via `replace`; no local checkout needed.

`git-pkgs/gittuf@silo` (5 commits over upstream `6f382ee`):

- `87d7697` `DetectDotGit:false` in `GetGoGitRepository` — bare repos open via go-git. silo dropped the `.git` gitfile.
- `07c6e23` `executor.withDir` replaces `os.Chdir` at four sites; `LoadRepository` uses `rev-parse --absolute-git-dir`. silo dropped `loadMu`. New `TestLoadRepositoryConcurrent` passes under `-race`.
- `08784f8` `WithRecordSigningKeyBytes` / `WithAnnotateSigningKeyBytes` route through the existing `CommitUsingSpecificKey` path. silo's `PostReceive` now writes a forge-signed witness annotation; `gittuf verify-ref` in a fresh clone still passes with it present.
- `2f700e9` go-git v5 → v6 (`PGPSignature`→`Signature`, `config.user` literal). `verifyGitsignSignature` moved behind `//go:build gitsign` so the default build doesn't link `sigstore/gitsign` → go-git v5. This is what gets the binary to one go-git.
- `02de542` `replace go-git/v6 => ../go-git-fork` (ignored when consumed as a module; silo's top-level replace governs).

`git-pkgs/go-git@silo` (1 commit over `v6.0.0-alpha.4`):

- `9661cc6` `ReceivePackHooks{PreReceive, PostReceive}` on `ReceivePackRequest`. `PreReceive` runs after unpack, before `updateReferences`, with a band-2 progress writer; non-nil error fills every ref's `ng` status. Replaces the `// TODO: support hooks` comment.

silo deltas: `internal/receive` collapsed from ~260 to 121 lines (adapter over `transport.ReceivePack`); `Advertise` gone; `internal/http/git` and `internal/ssh` rewritten on `transport.UploadPack` (dropped session/loader/`decodeHaves`). Net across the v6 port + collapse: -909 lines. `hooks.Builtin.PreReceive` now applies refs, runs `VerifyRef`, then rolls back before returning so go-git's `updateReferences` (which rejects Create-on-existing) applies cleanly; the flock spans through `PostReceive` so the gap is invisible.

Verified: all three txtar pass, `-race -shuffle=on` green, lint/govulncheck/deadcode/gosec clean, `go version -m` on the binary shows exactly `go-git/v6 => git-pkgs/go-git/v6` and `gittuf => git-pkgs/gittuf`, no v5.

Two incidental test-isolation findings filed in `GITTUF-NOTES.md` (`TestCanSign` leaks global `user.signingKey`) and `GOGIT-NOTES.md` (`Worktree.Commit` reads global `commit.gpgSign`).

Not yet done: upstream PRs (waiting on ask). gittuf is declared as a `tool` in silo's `go.mod` so `go build github.com/gittuf/gittuf` works through the replace without a local checkout; `go install github.com/git-pkgs/gittuf@silo` can't be used because the fork's `module` line is still `github.com/gittuf/gittuf` (changing it would mean rewriting every internal import and break upstreamability).
