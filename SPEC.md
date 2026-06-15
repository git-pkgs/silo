# silo implementation spec — gittuf-native forge

This file is the executable companion to `plan-01-gittuf-forge.md`. Read that first for why; this is what and done-when. Work through the milestones below in order: find the first one whose acceptance tests don't pass, work until they do, move on.

## Resolved decisions

- Module path: `github.com/git-pkgs/silo`
- Go: 1.26 (`go 1.26` in go.mod, `toolchain` unset)
- Licence: MIT. `LICENSE` file from `https://raw.githubusercontent.com/spdx/license-list-data/main/text/MIT.txt`, copyright line `2026 Andrew Nesbitt`. proxy integration later stays separate-process over HTTP.
- Push-rejection error format: multi-line over sideband-64k band 2, one fact per line. Exact shape:
  ```
  silo: rejected <refname>
    rule '<rule-name>' requires <threshold> of: <principal>, <principal>, ...
    you pushed as: <user> (<key-fingerprint>) — <in set|not in principal set>
    approvals on record: <n>/<threshold>
    policy: <base-url>/<owner>/<repo>/policy#<rule-name>
  ```
  Followed by report-status `ng <refname> policy` so git renders `! [remote rejected] ... (policy)`.
- Test layout: `internal/<pkg>/<pkg>_test.go` for unit tests, `testdata/testscript/*.txtar` for end-to-end via `rogpeppe/go-internal/testscript`. Fixtures (captured wire bytes, keys) under `testdata/fixtures/`.

## Quality bar

A milestone is not done until all of the following pass. Add a `Makefile` with these as targets during the receive-pack milestone and keep it green.

Tests and coverage:

- `go test -race -shuffle=on ./...` passes.
- `go test -cover ./internal/...` reports ≥ 80% per package; `internal/receive` and `internal/gittuf` ≥ 90%. Enforce with `go test -coverprofile=cover.out ./internal/... && go tool cover -func=cover.out | tail -1` and a check script.
- Every exported func in `internal/receive`, `internal/gittuf`, `internal/signer` has at least one direct test; table-driven where there's more than one case.

Lint and static analysis:

- `golangci-lint run --enable gocritic,gocognit,gocyclo,maintidx,dupl,mnd,unparam,ireturn,goconst,errcheck ./...` clean.
- `govulncheck ./...` clean.
- `deadcode ./...` clean.
- `gosec -quiet ./...` clean, or each finding suppressed with a `// #nosec Gxxx -- <reason>` comment that names the reason.

Benchmarks:

- Each package that sits in the request path has at least one `Benchmark*` with `b.ReportAllocs()`. Minimum set once gittuf verification is wired: `BenchmarkReceive_SingleRef` (replay the fixture), `BenchmarkPktlineRead`, `BenchmarkVerifyRef` (warm policy), `BenchmarkWitness`.
- `go test -bench . -benchmem -run ^$ ./internal/... | tee bench.txt` is committed at the end of each milestone so regressions are visible. No hard thresholds yet, but a >2× regression in ns/op or allocs/op between milestones is a finding to investigate, not ignore.

Security review:

- `go run github.com/git-pkgs/capcheck@latest ./cmd/silo` after each milestone; any new capability (network, exec, filesystem outside `$SILO_DATA`) gets a one-line justification appended under Findings. The baseline once SSH push works is: net (listeners), filesystem (`$SILO_DATA`), no exec, no cgo.
- Checklist, recorded under Findings as each item becomes applicable:
  - `internal/receive`: every `io.Reader` from the wire is wrapped in a limit; no `io.ReadAll` on client streams; pkt-line length field validated before allocating.
  - `internal/ssh`: exec command parsing rejects anything that isn't exactly `git-upload-pack` or `git-receive-pack` followed by a single quoted path; the path is cleaned and confined under `repos/`.
  - `internal/hooks`: `flock` is acquired before any gittuf state is read and released on every return path; `recover()` wraps `VerifyRef` and `InvokeHooksForStage`; the forge key file is `0600` and refused otherwise.
- Fuzz targets listed per milestone run for at least 1 minute locally (`go test -fuzz=Fuzz -fuzztime=60s`) with no crashes before the milestone closes; corpus additions are committed.

Documentation:

- All user-facing documentation (README, `docs/*`, godoc, cobra `Long` strings) describes the system as it currently exists, written as if for the first time. Never reference implementation history, prior approaches, milestones, decisions that were reversed, or "we used to / now we / this was changed because". A reader should not be able to tell the docs were written incrementally. If a design choice needs justifying, state the constraint it satisfies, not the path that led to it. Process notes belong in Findings or `GITTUF-NOTES.md`, never in `README.md` or `docs/`.
- `README.md` exists once `silo serve` exists and is kept current. It covers, in this order: one-paragraph what-and-why (the gittuf-native forge thesis, two sentences), install (`go install` and binary), quick start (the smallest sequence of commands that gets from nothing to a verified push: `silo keygen`, `silo serve`, `silo admin user create`, `gittuf trust init`, `git push`, `gittuf verify-ref`), the CLI command list, and configuration (flags and `SILO_*` env). Keep it under 200 lines; anything longer goes in `docs/`. Follow the writing rules in `~/.claude/CLAUDE.md` (no emoji, no banned words, flowing prose over bullet farms, no marketing headers).
- `docs/` holds the longer pieces, one file per topic, written as they become true:
  - `docs/architecture.md` once `silo serve` exists: the receive pipeline diagram in ASCII, the disk layout, the package map. Condensed from `plan-01-gittuf-forge.md`; that file is design-time and can drift, this one tracks the code.
  - `docs/trust-model.md` once gittuf verification is wired: owner-signs-root, witness vs authoriser, what `gittuf verify` proves and what it doesn't, the import-genesis caveat. Written for someone evaluating whether to run silo, not for a contributor.
  - `docs/errors.md` once gittuf verification is wired: every push-rejection message silo can emit, what it means, what to do about it. The multi-line format is the first entry.
  - `docs/api.md` once `/api/v1` exists: the surface with request/response examples; mark which endpoints are Gitea-compatible.
  - `docs/deploy.md` once anon-read caching exists: single-binary, systemd unit, the one-writer/N-reader topology, reverse proxy config, what to back up (`$SILO_DATA`).
- Every `cmd/silo` subcommand has a useful `Short` and `Long` in its cobra definition; `silo <cmd> --help` is the source of truth and `docs/cli.md` is generated from it (`cobra/doc.GenMarkdownTree`), not hand-written.
- Godoc: every exported type and func in `internal/receive`, `internal/gittuf`, `internal/signer` has a doc comment. `go doc ./internal/receive` should read as a usable reference.

End-to-end smoke:

- `testscript_test.go` at the repo root drives every `testdata/testscript/*.txtar` against a freshly built `silo` binary (built into `t.TempDir()`, exposed via `testscript.Params.Cmds`). `go test ./...` therefore runs unit and smoke together.
- A `make smoke` target builds the binary and runs only the txtar suite, for quick iteration.
- The smoke suite must pass with `-count=3` to catch order-dependence and port races (use `:0` listeners and surface the bound port via a status file the txtar `waitfor` reads).

## Bootstrap

```
git init
curl -sL https://raw.githubusercontent.com/spdx/license-list-data/main/text/MIT.txt > LICENSE   # then set the copyright line
go mod init github.com/git-pkgs/silo
mkdir -p cmd/silo internal testdata/testscript testdata/fixtures
```

Pinned direct deps (do not bump without re-running R1-R4 and updating their citations):

- `github.com/go-git/go-git/v5` — `v5.19.1`
- `github.com/gittuf/gittuf` — `6f382ee5c02943dee0195af6bee751a2d24a6533` (2026-06-14), since `experimental/` is unstable by name.
- `github.com/gliderlabs/ssh`, `modernc.org/sqlite`, `github.com/spf13/cobra`, `github.com/rogpeppe/go-internal` — latest tagged at the time their milestone adds them.

The `gittuf` CLI is needed for the verification txtar. Build it from the pinned module into the testscript `Cmds` map rather than relying on ambient PATH:

```go
gittufBin := buildOnce(t, "github.com/gittuf/gittuf")  // go build -o tmpdir from the pinned go.sum
```

Do not add a dep before the milestone that needs it.

## Conventions

These hold for the whole run.

- Milestone ordering is strict. Do not start the next milestone until the current one's "Done when" line is satisfied, even if parts of the next look trivial.
- Commit freely as work progresses; small commits are fine. Never push. Never rewrite history (no amend, no rebase, no force).
- Do not add: web frameworks or routers (stdlib `net/http.ServeMux` only), config file parsing (flags + `SILO_*` env only for now), a `pkg/` directory, ORMs or query builders (write SQL), codegen beyond `cobra/doc`, JS/CSS toolchain, telemetry.
- Do add, once `silo serve` exists: a `Dockerfile` (multi-stage, `FROM golang:1.26 AS build` → `FROM gcr.io/distroless/static`, the binary and nothing else, `USER nonroot`, `VOLUME /data`, `ENTRYPOINT ["/silo","serve","--data","/data"]`) and `.github/workflows/ci.yml` running `go test -race -shuffle=on ./...`, the lint set, `govulncheck`, and `make smoke` on push and PR. Keep both current as milestones land.
- Stuck protocol: when gittuf or go-git can't do what a milestone assumes, implement the smallest shim that makes that milestone's acceptance tests pass, record the shape and size of the shim in `GITTUF-NOTES.md` (or Findings for go-git), and move on. Do not redesign the milestone. Do not open upstream issues or PRs.
- SSH host key: generate ed25519 into `$SILO_DATA/host_ed25519` on first `serve` if absent, `0600`, refuse to start if perms are wider.
- testscript scaffolding (`testscript_test.go` at repo root):
  - `Setup`: set `GIT_CONFIG_NOSYSTEM=1`, `GIT_AUTHOR_NAME=test`, `GIT_AUTHOR_EMAIL=test@test`, `GIT_COMMITTER_*` likewise; allocate two `:0` TCP listeners, export `SILO_HTTP`/`SILO_HTTP_PORT`/`SILO_SSH`/`SILO_SSH_PORT`, close the listeners (silo reopens them); `SILO_DATA=$WORK/data`; generate ed25519 keypairs `alice`, `bob`, `mallory` into `$WORK` (private + `.pub`).
  - `Cmds`: `silo` (built once into a temp dir), `gittuf` (built from the pinned module), `waitfor <addr>` (dial tcp every 50ms, fail after 5s), `silo-test-seed <bare-path>` (init bare, add one commit with `README.md`).
  - Background `exec ... &` processes are tracked and killed in `t.Cleanup`.
- Extraction candidates: when a package under `internal/` turns out to have no silo-specific imports and a clean boundary, append it to `EXTRACT.md` with the proposed `git-pkgs/*` name, the public surface, and who else would use it. Do not actually extract; `internal/` is fine. Expected candidates from the design doc: `internal/receive` + `internal/http/git` + `internal/ssh` → `git-pkgs/gitserve`, `internal/cache` → `git-pkgs/oidcache`, plus anything new that emerges.

## Pre-work: research tasks

Resolve these before writing any code and record the answers under "Findings" at the bottom. Each is a yes/no with a file:line citation.

- R1: Does `github.com/gittuf/gittuf/experimental/gittuf.LoadRepository` open a bare repo (no worktree) and do `VerifyRef` / `RecordRSLEntryForReference` work against it? Check `pkg/gitinterface/repository.go` for worktree assumptions. If no, note the minimum shim needed.
- R2: Can a gittuf delegation reference a principal by name with key material resolved from a separate signed metadata blob, or is the key always embedded by value in root? Check `internal/tuf/v02/` for the principal/key types.
- R3: Does go-git `plumbing/transport/server.ReceivePack` expose a hook between packfile-unpack and ref-update? Check `go-git/v5/plumbing/transport/server/server.go`. Expected answer: no, which is why receive-pack is owned below.
- R4: Does go-git `plumbing/protocol/packp.AdvRefs` let arbitrary capability strings be set, so `bundle-uri` can be advertised? Check `packp/advrefs.go` and `capability/capability.go`.

---

## Milestone: own receive-pack

Goal: a `ReceivePack(ctx, repo, r io.Reader, w io.Writer, hooks Hooks) error` that speaks git's receive-pack protocol over arbitrary streams, with a callback between unpack and ref application.

Deliverables:

- `internal/receive/receive.go` — the entry point and the `Hooks` interface.
- `internal/receive/proto.go` — pkt-line read/write, ref-update command parsing, report-status encoding. Reuse `go-git/v5/plumbing/format/pktline` and `plumbing/protocol/packp` types where they fit; do not reuse `transport/server`.
- `internal/receive/unpack.go` — stream the packfile from `r` into the repo's `storer.EncodedObjectStorer` via `go-git/v5/plumbing/format/packfile.UpdateObjectStorage`. Enforce `MaxPackBytes` (default 512 MiB) and `MaxObjects` (default 100 000) read from a `Limits` struct; exceeding either aborts with `ng * unpack-limit`.
- `internal/receive/errors.go` — `RejectionError{Ref, Rule, Threshold, Principals, Pusher, PusherKey, Approvals, PolicyURL}` with a `Sideband() []string` method producing the multi-line format above, and a `Status() string` returning `policy` for the report-status reason.
- `testdata/fixtures/push-single-ref.bin` — bytes of a real `git push` of one commit to one ref, captured with `GIT_TRACE_PACKET=1` against a throwaway `git daemon`, for replay in tests.

Wire sequence the implementation must follow:

1. Write ref advertisement: for each ref in the repo, one pkt-line `<oid> <refname>`, first line carries `\0<capabilities>` where capabilities include `report-status`, `side-band-64k`, `delete-refs`, `quiet`, `agent=silo/<ver>`. If repo is empty, advertise `<zero-oid> capabilities^{}` with the cap list. Flush-pkt.
2. Read command list: pkt-lines of `<old-oid> SP <new-oid> SP <refname>`, first line carries `\0<client-caps>`. Stop at flush-pkt. Build `[]RefUpdate{Name, Old, New plumbing.Hash}`.
3. If any `New` is non-zero, a packfile follows on `r`. Unpack it into storage. If all updates are deletes, no pack.
4. Call `hooks.PreReceive(ctx, repo, updates)`. If it returns a `RejectionError`, write its `Sideband()` lines on band 2, then report-status with `unpack ok` and `ng <ref> <reason>` for each ref, and return nil (the protocol completed; the push was refused).
5. On `PreReceive` success, apply each ref update via `repo.Storer.SetReference` (or `RemoveReference` for deletes). All-or-nothing: if any set fails, roll back the ones already applied and report `ng` for all.
6. Call `hooks.PostReceive(ctx, repo, updates)`. Errors here are logged, not surfaced to the client.
7. Write report-status: `unpack ok`, then `ok <ref>` per applied update, flush-pkt. All wrapped in sideband band 1 if `side-band-64k` was negotiated.

`Hooks` interface:

```go
type RefUpdate struct {
    Name     plumbing.ReferenceName
    Old, New plumbing.Hash
}
type Hooks interface {
    PreReceive(ctx context.Context, repo *git.Repository, updates []RefUpdate) error
    PostReceive(ctx context.Context, repo *git.Repository, updates []RefUpdate)
}
```

Acceptance (`go test ./internal/receive/...`):

- `TestReceive_SingleRef`: in-memory go-git repo, replay `push-single-ref.bin`, assert ref exists at expected OID and report-status bytes match golden.
- `TestReceive_PreReceiveReject`: hook returns a `RejectionError`; assert ref unchanged, sideband lines match the resolved format, report-status is `ng refs/heads/main policy`.
- `TestReceive_Delete`: push a delete (zero new-oid, no pack); assert ref removed.
- `TestReceive_PackTooLarge`: set `MaxPackBytes=1`; assert abort with `unpack-limit` and no objects written.
- `TestReceive_Atomic`: two ref updates, second fails in `SetReference` (simulate via a storer wrapper); assert first is rolled back.
- `FuzzCommandList`: fuzz the pkt-line command parser; must not panic.

Done when: tests above pass, `go vet ./...` clean, a real `git push` against `ReceivePack` wired to a `net.Pipe` succeeds end to end (covered by the SSH-push smoke test below).

---

## Milestone: serve and HTTP clone

Goal: `silo serve` listens on HTTP, anonymous `git clone http://.../owner/repo.git` of a manually-placed bare repo works.

Deliverables:

- `cmd/silo/main.go`, `cmd/silo/serve.go` — cobra root and `serve` subcommand. Flags: `--data` (default `./silo-data`), `--http` (default `:8080`).
- `internal/config/config.go` — `Config{DataDir, HTTPAddr, SSHAddr, BaseURL string}` loaded from flags and env (`SILO_*`).
- `internal/gitstore/gitstore.go` — `Open(dataDir) *Store`; `(*Store).Repo(owner, name) (*git.Repository, error)` opening `<dataDir>/repos/<owner>/<name>.git` as bare via `go-git/v5.PlainOpenWithOptions(..., {DetectDotGit: false})`; `(*Store).Init(owner, name)` creating a bare repo.
- `internal/http/git/git.go` — `Handler(store) http.Handler` serving:
  - `GET /:owner/:repo.git/info/refs?service=git-upload-pack` → smart advertisement via `go-git/v5/plumbing/transport/server.NewServer(loader).NewUploadPackSession(...)` and `AdvertisedReferencesContext`.
  - `POST /:owner/:repo.git/git-upload-pack` → `UploadPack` on the same session.
  - 404 on `git-receive-pack` (HTTP is anon-read-only).
  The `transport.Loader` maps endpoint path → `gitstore.Repo`.
- `Dockerfile`, `.github/workflows/ci.yml`, `README.md`, `docs/architecture.md`, `Makefile` — see Conventions and Quality bar.

Acceptance (`testdata/testscript/01_clone.txtar`):

```
# 01_clone: anonymous HTTP clone of a pre-seeded bare repo
exec silo-test-seed $SILO_DATA/repos/alice/demo.git   # helper: git init --bare + one commit
exec silo serve --data $SILO_DATA --http $SILO_HTTP &
waitfor $SILO_HTTP

exec git clone http://$SILO_HTTP/alice/demo.git out
exists out/README.md

# receive-pack over HTTP must refuse
! exec git -C out push origin HEAD:refs/heads/x
stderr '404'
```

Plus `TestLoader` unit test: unknown repo → `transport.ErrRepositoryNotFound`.

Done when: `01_clone.txtar` passes via `testscript.Run`, `silo serve` exits cleanly on SIGINT.

---

## Milestone: SSH push

Goal: `silo admin user create`, register an SSH key, `git push` over SSH lands via the owned receive-pack with a no-op hook.

Deliverables:

- `internal/store/store.go` — `Open(dataDir) (*Store, error)` opening `<dataDir>/silo.db` (modernc.org/sqlite), schema applied on first open. Tables: `users(id, name, created_at)`, `ssh_keys(id, user_id, fingerprint, pubkey, created_at)`, `tokens(id, user_id, hash, created_at)`, `repos(id, owner, name, created_at)`, `repo_members(repo_id, user_id, role)`, `jobs(id, repo_id, kind, state, payload, attempts, updated_at)`. CRUD funcs: `CreateUser`, `AddSSHKey`, `UserBySSHFingerprint`, `CreateRepo`, `RepoByPath`.
- `cmd/silo/admin.go` — `silo admin user create <name> --ssh-key <path>`; `silo admin repo create <owner>/<name>`.
- `internal/ssh/ssh.go` — `Serve(ctx, addr, store, gitstore, hooks)` using `gliderlabs/ssh`. `PublicKeyHandler` computes SHA256 fingerprint, looks up `UserBySSHFingerprint`, stashes user in `ssh.Context`. `Handler` parses the exec command (`git-upload-pack '<path>'` or `git-receive-pack '<path>'`), resolves the repo, and:
  - upload-pack → go-git `UploadPack` session over `s` / `s.Stderr()`.
  - receive-pack → `receive.ReceivePack(ctx, repo, s, s, hooks)` with the resolved user attached to ctx.
- `internal/receive/context.go` — `WithPusher(ctx, Pusher{User, KeyFingerprint}) / PusherFrom(ctx)`.
- A no-op `Hooks` implementation in `cmd/silo/serve.go` for now: `PreReceive` returns nil, `PostReceive` no-op.

Acceptance (`testdata/testscript/02_push.txtar`):

```
# 02_push: SSH push lands, HTTP clone sees it
exec silo admin user create alice --ssh-key $WORK/alice.pub --data $SILO_DATA
exec silo admin repo create alice/demo --data $SILO_DATA
exec silo serve --data $SILO_DATA --http $SILO_HTTP --ssh $SILO_SSH &
waitfor $SILO_SSH

env GIT_SSH_COMMAND='ssh -i $WORK/alice -o StrictHostKeyChecking=no -p $SILO_SSH_PORT'
exec git init repo
exec git -C repo commit --allow-empty -m one
exec git -C repo remote add origin ssh://git@localhost:$SILO_SSH_PORT/alice/demo.git
exec git -C repo push origin main
stdout 'main -> main'

exec git clone http://$SILO_HTTP/alice/demo.git out
exec git -C out log --oneline
stdout 'one'

# unknown key rejected
env GIT_SSH_COMMAND='ssh -i $WORK/mallory -o StrictHostKeyChecking=no -p $SILO_SSH_PORT'
! exec git -C repo push origin main:x
```

Unit: `TestUserBySSHFingerprint` round-trip; `TestSSHExecParse` for the `git-receive-pack 'owner/repo.git'` quoting variants git emits.

Done when: `02_push.txtar` passes, `silo.db` schema matches the table list above (`TestSchema` asserts via `pragma table_list`).

---

## Milestone: gittuf verification and witnessing

Goal: repo creation produces an unsigned policy skeleton; pushes are refused until the owner signs root; thereafter receive verifies policy and witnesses the RSL. The push-rejection error format is exercised end to end.

Deliverables:

- `internal/signer/signer.go` — `type Signer interface { Sign(data []byte) ([]byte, error); PublicKey() crypto.PublicKey; ID() string }` and `Load(cfg) (Signer, error)`. One backend for now: `ed25519` reading `<dataDir>/forge.key`. `cmd/silo/keygen.go` writes a new key there; `cmd/silo/pubkey.go` prints it in SSH authorized_keys format and as a gittuf principal JSON.
- `internal/gittuf/gittuf.go` — wrapper holding a `*expgittuf.Repository` per bare repo (where `expgittuf` is `github.com/gittuf/gittuf/experimental/gittuf`). Funcs:
  - `InitSkeleton(repo, ownerKey, forgeKey)` → writes `refs/gittuf/policy-staging` with root naming `ownerKey` as root + authorising on `git:refs/heads/*` and `git:refs/tags/*`, and `forgeKey` as a separate witness principal not in any authorising set. Uses `expgittuf` policy builders; if those require a signed root to write anything, write the staged metadata as a plain blob and document the format here.
  - `HasSignedRoot(repo) bool` → true iff `refs/gittuf/policy` exists and `expgittuf.HasPolicy()` is true.
  - `Verify(ctx, repo, updates, pusher) (*Verdict, error)` → for each update, call `expgittuf.VerifyRef`; on failure, build a `receive.RejectionError` populated from `ListRules`/`ListPrincipals` for the matching rule. `Verdict{Ref, Rule string; Principals []string; Threshold int; Met bool}` per update.
  - `Witness(ctx, repo, updates, pusher, signer)` → for each update, `RecordRSLEntryForReference` then `RecordRSLAnnotation` with message `silo: pushed by <pusher.User> via <pusher.KeyFingerprint>`, signed by `signer`. Skip if `isDuplicateEntry` reports the client already recorded it; in that case annotate only.
- `internal/hooks/builtin.go` — a `receive.Hooks` impl: `PreReceive` takes `flock` on `<repo>/silo.lock`, checks `HasSignedRoot` (if false, reject with a fixed "repo not initialised: run `gittuf trust init` and push refs/gittuf/policy" message), runs `gittuf.Verify`, runs `expgittuf.InvokeHooksForStage(PrePush)`. `PostReceive` runs `gittuf.Witness` then releases the lock. Lock is also released on any `PreReceive` return.
- `cmd/silo/admin.go` — `repo create` now calls `gittuf.InitSkeleton` after `gitstore.Init`.
- `docs/trust-model.md`, `docs/errors.md` — see Quality bar.

Acceptance (`testdata/testscript/03_gittuf.txtar`):

```
# 03_gittuf: policy gates pushes, RSL is witnessed
exec silo keygen --data $SILO_DATA
exec silo admin user create alice --ssh-key $WORK/alice.pub --data $SILO_DATA
exec silo admin repo create alice/demo --data $SILO_DATA
exec silo serve --data $SILO_DATA --http $SILO_HTTP --ssh $SILO_SSH &
waitfor $SILO_SSH
env GIT_SSH_COMMAND='ssh -i $WORK/alice -o StrictHostKeyChecking=no -p $SILO_SSH_PORT'

# push before root is signed: refused with the init message
exec git init repo
exec git -C repo commit --allow-empty -m one
exec git -C repo remote add origin ssh://git@localhost:$SILO_SSH_PORT/alice/demo.git
! exec git -C repo push origin main
stderr 'repo not initialised'
stderr 'gittuf trust init'

# owner signs root locally and pushes policy
exec gittuf -C repo trust init --key $WORK/alice
exec gittuf -C repo policy init --key $WORK/alice
exec git -C repo push origin 'refs/gittuf/*:refs/gittuf/*'

# now a normal push works and is witnessed
exec git -C repo push origin main
exec git clone http://$SILO_HTTP/alice/demo.git verify
exec git -C verify for-each-ref refs/gittuf/reference-state-log
stdout 'refs/gittuf/reference-state-log'
exec gittuf -C verify verify-ref main

# bob is not in policy: push to main rejected with the multi-line format
exec silo admin user create bob --ssh-key $WORK/bob.pub --data $SILO_DATA
env GIT_SSH_COMMAND='ssh -i $WORK/bob -o StrictHostKeyChecking=no -p $SILO_SSH_PORT'
exec git -C repo commit --allow-empty -m two
! exec git -C repo push origin main
stderr 'silo: rejected refs/heads/main'
stderr 'requires 1 of: alice'
stderr 'you pushed as: bob'
stderr '/alice/demo/policy#'

# concurrent pushes to two branches: RSL stays linear
# (see TestConcurrentRSL unit test; testscript can't easily race)
```

Unit:

- `TestRejectionError_Sideband`: golden test of the multi-line format.
- `TestConcurrentRSL`: two goroutines call the `PreReceive`/ref-apply/`PostReceive` sequence on the same repo for different branches; assert the RSL ref afterwards is a single linear chain of length 2 (walk parents, no fork).
- `TestWitnessSkipsDuplicate`: pre-seed an RSL entry for the update as if a gittuf-aware client pushed it; assert `Witness` adds an annotation only, not a second entry.
- `TestInitSkeleton`: assert `refs/gittuf/policy-staging` exists, `HasSignedRoot` is false, and the staged root names the owner key and not the forge key in the `refs/heads/*` authorising set.

Done when: `03_gittuf.txtar` passes against a `gittuf` binary built from the pinned module, `gittuf verify-ref main` in the cloned `verify` dir exits 0, and the four unit tests pass. Record under Findings whether `expgittuf` needed a worktree (R1) and what shim, if any, was added.

Two wrinkles are expected here. Treat them as findings to record, not failures to redesign around.

The skeleton→owner-init handoff: silo stages a policy naming the forge key as witness, but `gittuf trust init` run locally by the owner doesn't read that staging ref and will produce a root that doesn't mention the forge key. The txtar will reveal whether `gittuf verify-ref` then accepts silo's witness annotations or not. If not, the options are (a) the owner adds the forge principal explicitly (`gittuf trust add-...`; document the extra command in README and `docs/trust-model.md`), or (b) witness annotations are verified out-of-band against `silo pubkey` rather than via policy. Do (a) and record in `GITTUF-NOTES.md` what "witness" maps to in gittuf's actual schema versus this spec's use of the word; this is likely the first substantive entry there.

The refs/RSL crash window: the receive sequence applies refs then calls `Witness`, both under the same `flock`, but a crash between them leaves the ref tip ahead of the RSL. Git can't make two ref updates atomic, so this window exists regardless of ordering. Keep the current order (refs then RSL); add a startup check in `serve` that walks each repo and logs a warning if any ref tip isn't covered by the latest RSL entry for that ref; and state the window and its detection in `docs/trust-model.md` ("a crash during push can leave a ref ahead of its RSL entry; silo logs this on next start and the next successful push to that ref closes the gap; a verifying client sees it as an unrecorded tip").

---

## gittuf feedback

silo is likely the first project to embed `experimental/gittuf` as a library on the server side of receive-pack. Friction, bugs, and gaps found here are upstreamable. Throughout all milestones, append to `GITTUF-NOTES.md` whenever you hit one of:

- API friction: something `experimental/gittuf` exposes that's awkward for this use (needs a worktree, assumes a remote, takes a CLI-shaped string where a typed value would fit, etc). Note what you did to work around it.
- Missing surface: something needed that's in `internal/` or not exposed at all. Note whether you vendored, reimplemented, or shelled out.
- Bugs: panics, incorrect verification results, RSL corruption, anything where gittuf's behaviour didn't match its docs or the design doc. Include a minimal reproduction.
- Opportunities: things gittuf could add that would make silo (or any forge integration) simpler. e.g. "a `Verdict` type returned from `VerifyRef` instead of just `error`", "bare-repo `RecordRSLEntry` without the commit-signing path".
- Protocol questions: places where the spec/design doc and gittuf's actual model disagree and it's not clear which is right.

One entry per finding, with the gittuf file:line that's relevant and the silo file:line where it bit. Don't fix gittuf in place; record and work around. The file is for sending upstream later, so write each entry so it makes sense to someone who hasn't read silo's code.

## Findings

(Append R1-R4 answers here with file:line citations before writing any code, and any deviations from this spec discovered during implementation. gittuf-specific observations go in `GITTUF-NOTES.md`, not here.)

R1: yes, with caveats. `pkg/gitinterface/repository.go:56` `LoadRepository` resolves gitDir via `git rev-parse --git-dir`, which works on bare repos. But: (a) it requires the `git` binary on PATH (`repository.go:58` `exec.LookPath`), and every operation in `pkg/gitinterface` shells out to git (`blob.go:14`, `commit.go:50`, `log.go:22`, etc.) — silo is not pure-Go while it links gittuf; (b) `LoadRepository` does `os.Chdir` into the repo and back (`repository.go:72,75`), which is process-global and races under concurrent loads; (c) `IsBare()` at `repository.go:48` returns `!strings.HasSuffix(gitDirPath, ".git")`, which misreports `repos/owner/name.git` as non-bare. Shim: serialise `LoadRepository` calls behind a package-level mutex in `internal/gittuf`, and don't rely on `IsBare()`. The system-git requirement is accepted for now and noted in `docs/architecture.md`. See `GITTUF-NOTES.md` for the upstream-facing entries.

R2: no. `internal/tuf/tuf.go:84` `Principal` interface has `Keys() []*signerverifier.SSLibKey`; v02 `Person` (`internal/tuf/v02/tuf.go:33`) embeds `PublicKeys map[string]*Key` by value. `AssociatedIdentities` carries provider strings (Sigstore etc.) but key material is inline. Forge key rotation requires each repo's root holders to re-sign. The witness-only default keeps the blast radius small; documented in `docs/trust-model.md`.

R3: no. `go-git/v5@v5.19.1 plumbing/transport/server/server.go:238-264` `rpSession.ReceivePack` calls `writePackfile` then immediately `updateReferences` with no callback between (and a `//TODO: Implement 'atomic' update` comment at :252). Confirmed: silo owns receive-pack.

R4: yes. `plumbing/protocol/packp/capability/list.go:127-129` — `validate` for an unknown `Capability` only checks for empty arg strings, then `Add` stores it. `advrefs.go:36` `Capabilities *capability.List` is exported. `caps.Add(capability.Capability("bundle-uri"), uri)` will encode into the advertisement.

receive-pack milestone deviations:

- `Advertise(repo, w)` is split from `ReceivePack(ctx, repo, r, w, hooks, limits)` rather than one function doing both, so the same code serves SSH (caller does both on one conn) and smart-HTTP (two separate handlers). Spec step 1 belongs to `Advertise`.
- No `proto.go`: pkt-line, command parsing, and report-status encoding are taken wholesale from `packp.{AdvRefs,ReferenceUpdateRequest,ReportStatus}` and `sideband.Muxer`, leaving nothing for a separate file.
- No `testdata/fixtures/push-single-ref.bin`: tests generate wire bytes in-process via `packp.ReferenceUpdateRequest.Encode` + `packfile.NewEncoder`, which exercises the same `Decode` path and avoids a binary blob in the repo. Real-git interop is covered by `02_push.txtar` in the SSH milestone.
- `TestReceive_Atomic` exercises `applyUpdates` against a minimal `storer.ReferenceStorer` mock rather than a wrapped `*git.Repository`, since `storage.Storer` embeds eight interfaces and wrapping it for one method is more code than the function under test.
- `packp.ReferenceUpdateRequest.Decode` always sets `Packfile` to the remaining reader (`updreq_decode.go:198`), so `ReceivePack` gates `unpack` on whether any command has a non-zero `New` rather than on `Packfile != nil`.
- Security checklist for `internal/receive`: pkt-line length validated by go-git's `pktline.Scanner` (rejects len > 65520); `capReader` bounds packfile bytes; object count read from the 12-byte header before parsing; no `io.ReadAll` on the client stream (packfile streams through `packfile.UpdateObjectStorage`).
- `.golangci.yml` excludes `goconst`, `dupl`, `mnd`, `ireturn` from `_test.go` files: test tables and interface mocks trip these without indicating a real problem. Production code is held to the full set.
- capcheck deferred to the next milestone since there is no `cmd/silo` binary yet.

serve-and-HTTP-clone milestone deviations:

- testscript scaffolding builds the `silo` binary once at repo root and prepends its dir to PATH in `Setup`, rather than using `testscript.RunMain`: the RunMain re-exec runs from `$WORK` where there is no `go.mod`, so `go build` inside it fails. `gittuf` will be built the same way for the verification milestone.
- `01_clone.txtar` asserts `'only git-upload-pack'` for the HTTP push refusal, since git first issues `GET info/refs?service=git-receive-pack` and that handler's message is what surfaces.
- capcheck invocation is `go run github.com/git-pkgs/capcheck/cmd/capcheck@latest` (binary lives under `cmd/`). Baseline at this milestone (capcheck.lock.json): NETWORK (HTTP listener), FILES (`$SILO_DATA`), READ_SYSTEM_STATE/OPERATING_SYSTEM (os/signal, env), REFLECT/UNSAFE_POINTER/RUNTIME/SYSTEM_CALLS (transitive via go-git and stdlib net), ARBITRARY_EXECUTION and MODIFY_SYSTEM_STATE (transitive via go-git's ssh transport which can exec ssh-agent; not reachable from silo's code paths yet). No exec from silo itself; no cgo.
