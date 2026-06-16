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

gittuf at `6f382ee` depends on go-git v5; either pin gittuf-fork to v5 internally (it can coexist; different module paths) or bump gittuf-fork to v6 too. Prefer coexistence first; bumping gittuf is its own patch.

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
