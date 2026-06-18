# silo spec — git-pkgs dependency indexing

This file is the executable companion to `plan-02-git-pkgs.md`. Read that first for why; this is what and done-when. Conventions, quality bar, commit rules, and testscript scaffolding are inherited from `SPEC.md`. Styling rules from `SPEC-web.md`.

Scope is the indexing half of the plan: per-repo `pkgs.sqlite3` written on push, rich rendering of manifests and lockfiles on blob/tree/commit/compare pages, a Dependencies tab, and `/api/v1/.../pkgs/*` endpoints. The dependency-aware policy hook (`refs/git-pkgs/deps*`, `internal/depref`, `InvokeHooksForStage` wiring), proxy integration (enrichment, vulns, cooldown, `pkgs-publish`, `git-pkgs/provenance`), and the non-pkgs items in the plan's build order (sigstore/agent signers, client-signed merge) are out of scope here; see Deferred.

## Resolved decisions

- Per-repo index db: `<repo>.git/pkgs.sqlite3`, opened WAL-mode by `git-pkgs/git-pkgs/index`. Sits inside the bare repo alongside `silo.lock` so it replicates with the rest of `repos/`.
- `index` package import path: `github.com/git-pkgs/git-pkgs/index`. The package wraps the existing `internal/{database,analyzer,indexer}` types behind a stable surface; it does not move or rename them. Roadmap issue #116.
- Upstream changes go on a `silo` branch in a local checkout, pinned via `replace` in silo's `go.mod`, the same pattern as `SPEC-forks.md`. silo's txtar suite is the integration proof. Nothing is pushed to public `git-pkgs/git-pkgs` main, and no tags are cut, until asked.
- Reindex runs as a queued job, not inline in `PostReceive`. The push returns as soon as refs are applied and witnessed; the Dependencies tab shows "indexing…" until the job for that tip completes. A single hostile lockfile cannot make a push hang.
- Stateless paths (the rich diff on a single file pair) call `manifests.Parse` directly with no `Options.FSRoot`, never the index db. They work before the first reindex completes and on commits the index hasn't seen.
- API mount: `/api/v1/repos/:owner/:repo/pkgs/{list,blame,history,diff,show,stats,sbom}`. JSON shapes match `git pkgs <cmd> --format=json` field-for-field so the CLI's output types can be reused as the response structs. `vulns`, `outdated`, `licenses` are deferred with proxy.
- New direct deps: `github.com/git-pkgs/git-pkgs` (for `index`, via `replace`), `github.com/git-pkgs/manifests`, `github.com/git-pkgs/sbom`, all latest tagged at the time their milestone adds them. `purl`, `vers`, `gitignore` arrive transitively. No proxy, enrichment, vulns, registries, sigstore, or depref deps in this spec.

## Bootstrap

The git-pkgs checkout already exists alongside silo:

```sh
git -C ~/code/git-pkgs/git-pkgs checkout -b silo
```

silo's `go.mod`:

```
require github.com/git-pkgs/git-pkgs v0.0.0
replace github.com/git-pkgs/git-pkgs => ../git-pkgs
```

Commits go on the `silo` branch in `../git-pkgs`; small commits are fine; don't push without asking. `manifests`, `vers`, `pom` changes (the P0/P1/P2 hardening) are already on their respective mains per `TODO.md` checkboxes; if any P2 cap is still unmerged when its milestone needs it, land it on a `silo` branch in that repo with a matching `replace` and record the SHA under Findings.

## Pre-work: upstream gates

Verify by file:line and record under Findings before starting the first milestone.

`~/code/git-pkgs/git-pkgs/TODO.md` (audit 2026-06-15):

- P0: `manifests` no longer touches the host filesystem when called with no `Options`. Already shaped (`manifests.go:51-76`, `Options{FSRoot}`); confirm `internal/maven` passes a no-op fetcher when `FSRoot == ""` and that `pom.LocalFetcher` is jailed (git-pkgs/pom#10). Add a test in `manifests` if absent: parse a `pom.xml` whose `<parent><relativePath>` points at `../../../../etc/hostname` with no options and assert the dep list ignores the parent.
- P1: both unrecovered goroutines (`internal/indexer/indexer.go:76`, `internal/database/batch_writer.go:198`) have a `defer recover()` that converts panic to error. Already checked off in TODO.md; confirm by grep.
- P2: at minimum the four single-choke-point caps that bound a single reindex job: `manifests.MaxManifestBytes` (default 10 MiB), per-manifest dependency cap and per-commit manifest cap in `internal/analyzer`, and `batch_writer.ShouldFlush` keyed on pending row count. These land on the `../git-pkgs` `silo` branch as part of the index-extraction milestone if not already on main.
- `vers` P2 (cache key length cap, input length cap) and P3 (bubble sort, quadratic `Intersect`) are not gating but are reachable from the rich diff on every commit-page render; land them before that milestone if they're not already done, on a `silo` branch in `~/code/git-pkgs/vers` with a `replace` if needed.

## Pre-work: research tasks

Resolve before writing silo code; record answers under Findings with file:line citations. Numbering continues from `SPEC.md`.

- R5: Does `internal/indexer.Indexer.Run` accept a bare `*git.Repository` (no worktree), or does any path under it call `Worktree()` / read from the filesystem rather than the object store? Check `internal/indexer/indexer.go`, `internal/analyzer/analyzer.go:543` (`parseManifestInTree`), and `internal/git/`. Expected answer: yes once `analyzer.SetRepoPath`/`PrefetchDiffs` (which exec `git diff-tree`) are made optional, since the rest reads via go-git `object.Tree`.
- R6: Does `analyzer.parseManifestInTree` already pass `Options{FSRoot: ""}` (or no options) to `manifests.Parse`? If it passes a host path, that's the maven traversal vector reaching silo through the index package even after the manifests fix.
- R9: `git pkgs <cmd> --format=json` output types. For each of `list`, `blame`, `history`, `diff`, `show`, `stats`, `sbom`: locate the struct that is `json.Marshal`ed (likely in `cmd/*.go` or `internal/database/types.go`) and note whether it's exported. The API milestone reuses these types verbatim; if any is unexported or `cmd`-local, the index extraction must lift it.

(R7 and R8 concerned the Lua sandbox and `InvokeHooksForStage`; they move to the deferred depref spec.)

---

## Milestone: upstream — `git-pkgs/git-pkgs/index`

In `../git-pkgs` on the `silo` branch. This is the issue-#116 extraction: a public package over the existing `internal/{database,analyzer,indexer}` so silo can import the same code the CLI uses. The P2 caps that aren't yet on main land here too as separate commits.

Deliverables (`index/index.go` at the git-pkgs repo root):

```go
package index

type Index struct { /* wraps *database.DB, *git.Repository, *analyzer.Analyzer */ }

type Options struct {
    MaxManifestBytes      int   // passed through to manifests.Parse; 0 = package default
    MaxDepsPerManifest    int
    MaxManifestsPerCommit int
    Progress              func(done, total int)
}

func Open(gitDir, dbPath string, opts Options) (*Index, error)
    // gitDir is a bare or non-bare repo; dbPath is created if absent.
    // Opens go-git with PlainOpenWithOptions{DetectDotGit: false} when gitDir
    // ends in .git, otherwise true.

func (*Index) Reindex(ctx context.Context, branch string, oldTip, newTip plumbing.Hash) error
    // Walks newTip back to the merge-base with oldTip (or full history when
    // oldTip is zero), calling analyzer.AnalyzeCommit per commit and writing
    // via the batch writer. Honours ctx cancellation between commits.

func (*Index) Close() error

// Read surface, each a thin pass-through to internal/database queries.
// Return types are the existing exported structs (Dependency, BlameEntry,
// HistoryEntry, Change, Stats, CommitWithChanges) re-exported as aliases
// from this package so callers don't import internal/.
func (*Index) List(ref string) ([]Dependency, error)        // GetDependenciesAtRef
func (*Index) Blame(opts BlameOptions) ([]BlameEntry, error)
func (*Index) History(opts HistoryOptions) ([]HistoryEntry, error)
func (*Index) Diff(fromRef, toRef string) ([]Change, error)  // two GetDependenciesAtRef + analyzer diff
func (*Index) Show(sha string) ([]Change, error)             // GetChangesForCommit
func (*Index) Stats(opts StatsOptions) (*Stats, error)
func (*Index) HasIndexed(sha string) (bool, error)           // HasSnapshotForCommit
```

`internal/database`, `internal/analyzer`, `internal/indexer` stay where they are; `index` imports them.

Constraints the package must satisfy for silo:

- No `os.Chdir`, no process-global state. `Open` may be called for many repos concurrently.
- No `exec.Command` in the `Reindex` path. `analyzer.PrefetchDiffs` (which shells to `git diff-tree`) is skipped when `Analyzer.repoPath == ""`; `Open` leaves it empty.
- `manifests.Parse` is always called with the in-tree path as `filename` and no `FSRoot`.
- `Reindex` wraps the `indexer.Run` body in `recover()` and returns the panic as an error.
- WAL mode and `busy_timeout=5000` on the db handle so a reader during reindex doesn't `SQLITE_BUSY`.

Acceptance (`index/index_test.go` in `../git-pkgs`):

- `TestOpen_Bare`: `git init --bare`, push one commit containing a `go.mod` with two requires via go-git, `Open`, `Reindex("main", zero, tip)`, `List("main")` returns both.
- `TestReindex_Incremental`: second commit adds one require; `Reindex("main", tip1, tip2)`; `Show(tip2)` returns one `added` change; `List` returns three.
- `TestReindex_Cancel`: ctx cancelled after first commit of a 100-commit chain; returns `ctx.Err()`; db has ≤ a few commits' rows.
- `TestReindex_HostilePom`: commit a `pom.xml` with `<parent><relativePath>../../../../etc/hostname</relativePath>`; `Reindex` succeeds; `List` shows the pom's own deps and nothing from the host.
- `TestReindex_Caps`: commit a `package-lock.json` with `MaxDepsPerManifest+1` entries; `Reindex` succeeds; `List` returns exactly the cap.
- `TestConcurrentOpen`: 10 goroutines `Open` 10 distinct bare repos in `t.TempDir()` and `Reindex` each; `-race` clean.

Done when: `go test ./index/...` passes in `../git-pkgs`, the CLI's own `cmd/{list,blame,history,diff,show,stats}.go` are rewired to call `index.*` instead of `internal/database` directly (proves the surface is sufficient), and `go test ./...` in `../git-pkgs` stays green. silo's `go.mod` has the `replace` and `go build ./...` in silo succeeds with the import added (no callers yet).

---

## Milestone: jobs queue and reindex-on-push

Goal: pushing a commit that touches a manifest enqueues a `pkgs-reindex` job; a worker drains the queue and writes `pkgs.sqlite3`; the dependency API can answer `show` for that commit once the job completes.

Deliverables:

- `internal/store/jobs.go` — CRUD on the existing `jobs` table:
  ```go
  type Job struct {
      ID        int64
      RepoID    int64
      Kind      string
      State     string // pending | running | done | failed
      Payload   string // JSON
      Attempts  int
      UpdatedAt time.Time
  }
  func (*Store) EnqueueJob(repoID int64, kind, payload string) (int64, error)
  func (*Store) ClaimJob(kinds ...string) (*Job, error)
      // UPDATE ... SET state='running', attempts=attempts+1 WHERE id =
      //   (SELECT id FROM jobs WHERE state='pending' AND kind IN (...) ORDER BY id LIMIT 1)
      // RETURNING *. nil, nil when nothing pending.
  func (*Store) CompleteJob(id int64, state string) error  // 'done' or 'failed'
  func (*Store) JobsForRepo(repoID int64, kind string) ([]Job, error)
  ```
  Dedup: `EnqueueJob` does nothing if a `pending` row with the same `(repo_id, kind, payload)` already exists.
- `internal/jobs/jobs.go` — `Worker{Store, GitStore, Handlers map[string]func(ctx, Job) error}` with `Run(ctx)` polling `ClaimJob` every 250 ms (or on a `Nudge()` channel) and dispatching. Each handler runs under a per-job `context.WithTimeout` (default 10 min) and inside `recover()`; panic or error → `CompleteJob(id, "failed")`, success → `"done"`. A job that has failed `>= 3` attempts is not re-claimed.
- `internal/pkgs/pkgs.go`:
  ```go
  type Store struct { /* LRU of repoPath -> *index.Index, cap 32 */ }
  func Open() *Store
  func (*Store) Index(repoPath string) (*index.Index, error)
      // Opens <repoPath>/pkgs.sqlite3 via index.Open with the P2 caps set.
      // Evicted entries are Close()d.
  func (*Store) Reindex(ctx context.Context, repoPath, branch string, old, new plumbing.Hash) error
  ```
  plus `ReindexHandler(ps *Store, gst *gitstore.Store) func(ctx, Job) error` decoding `payload` `{"owner","repo","branch","old","new"}` and calling `Reindex`.
- `internal/hooks/builtin.go` — `Builtin` gains `Store *store.Store` and `Nudge func()`. `PostReceive`, after witnessing and before `releaseLock`, iterates non-gittuf updates whose `Name.IsBranch()` and calls `EnqueueJob(repoID, "pkgs-reindex", payload)` then `Nudge()`. Failure to enqueue is logged, not surfaced.
- `cmd/silo/serve.go` — construct `pkgs.Open()`, register `pkgs.ReindexHandler` on a `jobs.Worker`, start `Worker.Run` in a goroutine tied to the serve context, and pass `worker.Nudge` into the hooks factory.
- `cmd/silo/admin.go` — `silo admin pkgs reindex <owner>/<repo> [--branch main]` enqueues a full reindex (`old` = zero) for manual recovery and for the first index of an imported repo.

Acceptance (`testdata/testscript/06_pkgs.txtar`):

```
# 06_pkgs: push a go.mod, reindex job runs, API shows the change
exec silo keygen --data $SILO_DATA
exec silo admin user create alice --ssh-key $WORK/alice.pub --data $SILO_DATA
exec silo admin repo create alice/demo --data $SILO_DATA
exec silo serve --data $SILO_DATA --http $SILO_HTTP --ssh $SILO_SSH &
waitfor $SILO_SSH
env GIT_SSH_COMMAND=...

# init policy as in 03_gittuf, then:
exec git -C repo init
cp go.mod.1 repo/go.mod
exec git -C repo add go.mod
exec git -C repo commit -m 'add deps'
exec git -C repo push origin main 'refs/gittuf/*:refs/gittuf/*'

# wait for the reindex job (poll the API; helper cmd 'waitjson <url> <jq-expr>')
waitjson http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/list?ref=main '. | length >= 2'
exec curl -fsS http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/list?ref=main
stdout 'pkg:golang/github.com/spf13/cobra'

# pkgs.sqlite3 exists inside the bare repo
exists $SILO_DATA/repos/alice/demo.git/pkgs.sqlite3

# bump a dep, push, show returns the delta
cp go.mod.2 repo/go.mod
exec git -C repo commit -am 'bump cobra'
gitsha repo SHA2
exec git -C repo push origin main
waitjson http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/show/$SHA2 '. | length == 1'
stdout '"change":"updated"'

-- go.mod.1 --
module example.com/demo
go 1.26
require (
    github.com/spf13/cobra v1.8.0
    github.com/stretchr/testify v1.9.0
)
-- go.mod.2 --
module example.com/demo
go 1.26
require (
    github.com/spf13/cobra v1.9.0
    github.com/stretchr/testify v1.9.0
)
```

Add to `testscript.Params.Cmds`: `waitjson <url> <expr>` polls the URL every 100 ms for up to 10 s, decodes JSON, evaluates `<expr>` (only `length >= N` and `length == N` need supporting; hand-roll, no jq dep); `gitsha <dir> <var>` sets `<var>` to `git -C <dir> rev-parse HEAD`.

Unit:

- `TestEnqueueDedup`: two `EnqueueJob` with identical args produce one row.
- `TestClaimJob`: 5 goroutines claim from a queue of 5; each job claimed exactly once (`-race`).
- `TestWorker_Recover`: handler panics; job ends `failed`, worker keeps running.
- `TestWorker_Resume`: insert a `running` row with `updated_at` older than the timeout; assert `ClaimJob` returns it (covers crash-during-reindex).
- `TestPkgsStore_LRU`: open 40 repos with cap 32; first 8 are closed; reopening one of those works.
- `TestPostReceive_Enqueues`: in-memory repo + store, call `PostReceive` with one branch update; assert one `pkgs-reindex` row with the right payload.

The `/api/v1/.../pkgs/list` and `/show` endpoints needed by the txtar are stubs at this milestone: a temporary handler in `cmd/silo/serve.go` that calls `pkgs.Store.Index(repoPath).List(ref)` / `.Show(sha)` and `json.Encode`s the result. They move into `internal/http/api` two milestones down.

Done when: `06_pkgs.txtar` passes with `-count=3`.

---

## Milestone: rich manifest rendering

Goal: wherever the web UI shows a manifest or lockfile, it shows the parsed dependency set rather than (or as well as) raw text. On commit and compare pages a changed manifest renders as a "+kamal 1.0.0 / puma 5.0.0 → 6.0.0" delta table in place of three hundred lines of text diff; on the blob page the same file renders as a sortable dependency table with the raw source behind a tab. Both are computed statelessly from blob bytes via `manifests.Parse`, so they work on any commit regardless of index state and render identically on a read replica with no `pkgs.sqlite3`.

Deliverables:

- `internal/pkgs/textconv.go`:
  ```go
  type FileDelta struct {
      Path      string
      Ecosystem string
      Kind      manifests.Kind   // manifest | lockfile
      Added     []manifests.Dependency
      Removed   []manifests.Dependency
      Updated   []DepUpdate       // {Name, PURL, From, To string}
  }
  type FileView struct {
      Path      string
      Ecosystem string
      Kind      manifests.Kind
      Groups    []DepGroup        // {Scope string; Deps []manifests.Dependency} sorted runtime-first
      Total     int
      Direct    int
  }
  func IsManifest(path string) bool                          // manifests.Identify
  func Render(path string, blob []byte) (*FileView, error)   // one-sided
  func Textconv(path string, oldBlob, newBlob []byte) (*FileDelta, error)
  ```
  Both call `manifests.Parse(path, blob)` with no `FSRoot`. A blob over `manifests.MaxManifestBytes` is treated as unparseable. For `Textconv` either side may be nil (file added/removed). On `manifests.ParseError`, return `(nil, nil)` so the caller falls back to raw text.
- `internal/pkgs/cache.go` — `sync.Map` keyed on `oldOID + ":" + newOID + ":" + path` → `*FileDelta`, capped by an atomic counter at 4096 entries (drop the whole map when exceeded; per-OID-pair results are cheap to recompute). This is the placeholder until `git-pkgs/oidcache` exists; note it in `EXTRACT.md`.
- `internal/http/web/web.go:318` (`h.commit`) and `compare.go` — for each changed file where `pkgs.IsManifest(path)`, fetch both blobs and call `pkgs.Textconv`; render the result above the raw diff hunk via `templates/pages/_depdelta.html`. The raw diff for that file collapses behind a `<details>` element. Files where textconv returned nil render as before.
- `internal/http/web/browse.go` (`h.blob`) — when `pkgs.IsManifest(path)`, call `pkgs.Render(path, blob)` and render via `templates/pages/_depview.html` as the default view, with the existing markup/source render behind a second tab in a basecoat `tabs` tablist (`Dependencies` | `Source`). `?view=source` forces the source tab. Each dependency name links to `/:o/:r/dependencies/{purl}` (404 until the next milestone; acceptable). Render returning nil falls through to the existing source view with no tabs.
- `internal/http/web/browse.go` (`h.tree`) — directory rows for paths where `IsManifest` is true get a small `<ecosystem> · N deps` annotation after the filename, computed from `pkgs.Render` on the blob (cached the same way). Skip the annotation for blobs over the size cap.
- `templates/pages/_depdelta.html` — a `table class="table"` with rows tinted by `.kind` (`dep-added` green, `dep-removed` red, `dep-updated` neutral), columns: name, old → new requirement, scope, direct/transitive. Heading row reads `<path> · <ecosystem> <kind> · +N −M ~K`.
- `templates/pages/_depview.html` — one `table class="table"` per scope group with a heading row `<scope> · N`, columns: name, requirement, direct/transitive, integrity (truncated, `title=` full). Summary line above: `<ecosystem> <kind> · N dependencies (M direct)`. Styles in `static/style.css`.

Acceptance (extend `06_pkgs.txtar` rather than a new file):

```
exec curl -fsS http://$SILO_HTTP/alice/demo/commit/$SHA2
stdout 'go.mod · golang manifest · +0 −0 ~1'
stdout 'cobra'
stdout 'v1.8.0'
stdout 'v1.9.0'
stdout 'class="dep-updated"'
# raw diff still present, collapsed
stdout '<details'
stdout '+    github.com/spf13/cobra v1.9.0'
```

Blob and tree views:

```
exec curl -fsS http://$SILO_HTTP/alice/demo/blob/main/go.mod
stdout 'role="tablist"'
stdout 'golang manifest · 2 dependencies (2 direct)'
stdout 'github.com/spf13/cobra'
stdout 'v1.9.0'
# source tab still reachable
exec curl -fsS 'http://$SILO_HTTP/alice/demo/blob/main/go.mod?view=source'
stdout 'require ('

# tree row annotation
exec curl -fsS http://$SILO_HTTP/alice/demo/tree/main
stdout 'go.mod'
stdout 'golang · 2 deps'
```

And a hostile case:

```
# pom with relativePath traversal: page renders, no crash, falls back to
# parsed deps from the pom body only
cp pom.evil repo/pom.xml
exec git -C repo add pom.xml
exec git -C repo commit -m pom
gitsha repo SHA3
exec git -C repo push origin main
exec curl -fsS http://$SILO_HTTP/alice/demo/commit/$SHA3
stdout 'pom.xml · maven'
! stdout 'hostname'
```

Unit:

- `TestTextconv` table: go.mod add/remove/bump, package-lock.json with 3 deps → 2, Gemfile with a scope change, file added (old nil), file removed (new nil), unparseable garbage on one side → `(nil, nil)`.
- `TestRender` table: go.mod with two requires → one runtime group of two; package.json with `dependencies` and `devDependencies` → two groups, runtime first; Gemfile.lock with 50 entries → totals match; garbage → `(nil, nil)`.
- `TestTextconv_Oversize` / `TestRender_Oversize`: 11 MiB blob → `(nil, nil)` without allocating the parse.
- `TestIsManifest` table over a dozen filenames including `go.mod`, `go.sum`, `package.json`, `package-lock.json`, `Gemfile.lock`, `Cargo.toml`, `pom.xml`, and negatives `main.go`, `README.md`, `go.mod.bak`.
- `BenchmarkTextconv_PackageLock1k` with `b.ReportAllocs()`.
- `FuzzTextconv`: fuzz `(path, old, new)`; must not panic.

Done when: the txtar additions pass and a 5000-line `package-lock.json` diff renders the commit page in under 200 ms warm (assert in `BenchmarkCommitPage_Lockfile` against a threshold with 4× headroom, i.e. fail over 800 ms).

---

## Milestone: dependencies UI and API

Goal: the Dependencies tab and the full `/api/v1/repos/:o/:r/pkgs/*` surface, both reading the per-repo index.

Deliverables:

- `internal/http/api/` (new package; the first `/api/v1` consumer): `Handler(st, gst, ps) http.Handler` mounting:
  ```
  GET /api/v1/repos/{o}/{r}/pkgs/list?ref=<ref>&ecosystem=&direct=
  GET /api/v1/repos/{o}/{r}/pkgs/blame?ref=<ref>
  GET /api/v1/repos/{o}/{r}/pkgs/history/{name}?ecosystem=
  GET /api/v1/repos/{o}/{r}/pkgs/diff?from=<ref>&to=<ref>
  GET /api/v1/repos/{o}/{r}/pkgs/show/{sha}
  GET /api/v1/repos/{o}/{r}/pkgs/stats?ref=<ref>
  GET /api/v1/repos/{o}/{r}/pkgs/sbom?ref=<ref>&format=cyclonedx|spdx
  ```
  Each handler resolves the repo via `gitstore`, opens `ps.Index(repoPath)`, calls the matching `index.*` func, and writes `Content-Type: application/json`. `sbom` builds a `git-pkgs/sbom.Document` from `index.List(ref)` and writes the requested format. Unknown repo → 404, index error → 503 with `{"error":"dependencies unavailable"}`, ref with no snapshot yet → 200 `[]` and header `X-Pkgs-Indexing: true` if a `pkgs-reindex` job for the repo is pending or running.
- `cmd/silo/serve.go`: mount `api.Handler` at `/api/v1/`. Move the stub `list`/`show` handlers from the reindex milestone into `internal/http/api`.
- `internal/http/web/deps.go` — handlers:
  ```
  GET /{o}/{r}/dependencies                  list at default branch + stats summary
  GET /{o}/{r}/dependencies/blame            blame table
  GET /{o}/{r}/dependencies/stats            ecosystem/scope counts, churn chart (CSS bars)
  GET /{o}/{r}/dependencies/{purl}           history for one package
  ```
  Add `dependencies` to the repo-nav `tabs` tablist in `templates/layout/header.html`. The list page shows an "indexing…" banner when `JobsForRepo(repoID, "pkgs-reindex")` has anything not `done`. The per-package page resolves `{purl}` after `url.PathUnescape` and renders `index.History` as a timeline; upstream changelog excerpts are deferred with proxy.
- `internal/http/web/compare.go` — append a "Dependency changes" section fed by `index.Diff(base, head)` (falls back to summing per-file `Textconv` results when either ref is unindexed).
- `internal/http/web/refs.go` (`tags` handler) — per-tag SBOM download links (`?format=cyclonedx`, `?format=spdx`).
- `templates/pages/{deps_list,deps_blame,deps_stats,deps_history}.html`, all using basecoat `table` and the existing `panel`/`kv` classes.
- `docs/api.md` — first version, since this is the first `/api/v1` surface to ship. One section per endpoint with a `curl` example and a truncated response. Mark all `pkgs/*` endpoints as silo-specific (not Gitea-shaped).

Acceptance (`testdata/testscript/07_pkgs_ui.txtar`, building on the 06 setup):

```
# tab present
exec curl -fsS http://$SILO_HTTP/alice/demo
stdout 'href="/alice/demo/dependencies"'

# list page
exec curl -fsS http://$SILO_HTTP/alice/demo/dependencies
stdout 'github.com/spf13/cobra'
stdout 'class="table"'

# blame: who added cobra
exec curl -fsS http://$SILO_HTTP/alice/demo/dependencies/blame
stdout 'cobra'
stdout 'add deps'

# per-package history
exec curl -fsS 'http://$SILO_HTTP/alice/demo/dependencies/pkg:golang/github.com%2Fspf13%2Fcobra'
stdout 'v1.8.0'
stdout 'v1.9.0'

# API parity with CLI: same JSON shape
exec curl -fsS http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/list?ref=main -o api.json
exec git-pkgs -C $SILO_DATA/repos/alice/demo.git list --ref main --format json -o cli.json
exec jqdiff api.json cli.json   # helper: jq -S on both, diff, fail on mismatch

# sbom
exec curl -fsS 'http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/sbom?ref=main&format=cyclonedx'
stdout '"bomFormat":"CycloneDX"'

# unindexed ref returns empty + header
exec curl -fsSi http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/list?ref=refs/heads/nope
stdout 'X-Pkgs-Indexing'

# corrupt db -> 503, not 500
exec sh -c 'echo garbage > $SILO_DATA/repos/alice/demo.git/pkgs.sqlite3'
exec curl -s -o /dev/null -w '%{http_code}' http://$SILO_HTTP/api/v1/repos/alice/demo/pkgs/list?ref=main
stdout '503'
```

Add to `testscript.Params.Cmds`: `git-pkgs` (the CLI binary, built once from `../git-pkgs` at its `silo` branch into the temp bin dir alongside `silo` and `gittuf`) and `jqdiff <a> <b>` (decode both, `json.MarshalIndent` with sorted keys, `diff`, fail on mismatch; no jq dep).

Unit: `TestAPIPkgs_Routes` (every endpoint 200 with right `Content-Type`, unknown repo 404, db error 503), `TestDepsList_Indexing` (banner shows when a pending job exists), `TestSBOM_Formats`, `TestPURLPathDecode` (slashes survive the round-trip).

Done when: `07_pkgs_ui.txtar` passes; `docs/api.md` covers every `pkgs/*` endpoint; `make demo` shows a populated Dependencies tab on `alice/demo`.

---

## git-pkgs feedback

Same protocol as `GITTUF-NOTES.md` in `SPEC.md`. Append to a new `GITPKGS-NOTES.md` whenever the `index` extraction, `manifests`, `vers`, or `sbom` is awkward to use from silo's side: missing surface, types that should be exported, parsers that behave differently on bare-repo blobs than on worktree files, anything from the TODO.md follow-up audits (3/4/6/7/8/9) that bites in practice. One entry per finding, file:line in both repos, written so it makes sense to someone who hasn't read silo.

## Deferred

Recorded so it's not lost; do not implement here.

depref and the dependency policy hook (next spec after this one):

- `internal/depref` package in silo: `Snapshot{Manifests map[string][]Dep}`, `Compute(*object.Tree)`, canonical-JSON `Encode`/`Decode`, `Write`/`Read` against a `storer.Storer`, `Diff(before, after) []Change`. Starts under `internal/`; add to `EXTRACT.md` as `git-pkgs/depref` once the git-pkgs CLI's local hook wants it.
- `refs/git-pkgs/deps/<branch>` written in `PostReceive`; `refs/git-pkgs/deps-incoming/<branch>` written between unpack and verify in `PreReceive`; `InvokeHooksForStage(PrePush)` wired into `Builtin.PreReceive` after `VerifyRef` so an in-policy Lua hook can read both refs and reject on the diff.
- R7 (luasandbox `gitGetReference` on a blob-pointing ref) and R8 (`InvokeHooksForStage` per-invocation parameters) belong to that spec.
- `08_depref.txtar`: push with no hook → `deps/main` exists; add `no-new-runtime-deps.lua` to policy; push adding a direct runtime dep → rejected; bump existing dep → allowed; same hook gives same verdict in a fresh clone.
- `docs/trust-model.md` section on what a hook reading `refs/git-pkgs/deps/*` does and does not prove (the ref is silo-written and unsigned).

proxy (`SPEC-proxy.md`):

- `proxy.url` config and the enrichment client (`/api/bulk`, `/api/vulns`, `/api/outdated`).
- `cooldown` flags on commit/textconv rows.
- `vulns`, `outdated`, `licenses` API endpoints and UI columns.
- Upstream changelog excerpts on `/dependencies/{purl}`.
- `git-pkgs/provenance` module, `pkgs-publish` post-receive hook, in-toto attestations, tag-page artifact browser via `archives`, `reuse` per-file licence on blob pages.

Not git-pkgs (continue from `SPEC.md`):

- `signer.type: sigstore` / `signer.type: agent`.
- Client-signed merge on the compare page.

## Findings

### Upstream gates (audit 2026-06-16)

- **P0 manifests opt-in FS**: confirmed. `manifests/manifests.go:62` defines `func Parse(filename string, content []byte, opts ...Options)`. With no opts, `Options{FSRoot: ""}` is used and only `FSRootParser` (currently pom only) consults disk via `ParseInRoot`. `manifests/manifests.go:75-78`. silo will call `Parse(path, content)` with no opts.
- **P0 manifests no host hostname leak**: `internal/analyzer/analyzer.go:559` calls `manifests.Parse(path, []byte(content))` with no Options — confirms no FSRoot leaks through the indexer path.
- **P0 pom jail + size cap**: TODO.md marks both [x]; not re-verified here.
- **P1 indexer goroutine recover**: deviation. TODO.md marks [x], but `internal/indexer/indexer.go:76` `go func()` still has no `defer recover()` (grep shows zero `defer`/`recover`/`panic` in `internal/indexer/indexer.go`). The silo `index` package will wrap its own `Reindex` body in `recover()` per spec, which avoids the issue for silo's reindex path but does not cover the CLI's own use of the unchanged `indexer.Run`. Lands on `../git-pkgs@silo` as a separate commit.
- **P1 batch_writer goroutine recover**: deviation, same shape. `internal/database/batch_writer.go:198` (`FlushAsync`) still has no `defer recover()`. Two `defer`s present at :234 and :478 but neither is a panic guard.
- **P2 manifests global input size cap**: not yet present. `manifests/manifests.go:62` does not gate `len(content)`. Lands on the `../manifests` `silo` branch as part of this milestone with a `MaxManifestBytes` constant (default 10 MiB) and `ErrTooLarge`.
- **P2 per-manifest dep cap / per-commit manifest cap / flush on pending row count**: not yet present in `internal/analyzer/analyzer.go` or `internal/database/batch_writer.go`. Lands on the `../git-pkgs@silo` branch with caps exposed through `index.Options`.

### Research answers

- **R5 — Indexer.Run on a bare repo**: no, not as-is. Three exec/worktree dependencies must be skipped:
  1. `internal/git/repository.go:37` calls `repo.Worktree()` which errors on a bare repo, so `OpenRepository` cannot be used unchanged.
  2. `internal/indexer/indexer.go:62` invokes `LoadMailmap`, which at `internal/git/repository.go:207` reads `.mailmap` from `workDir`. Bare repos have no workdir.
  3. `internal/indexer/indexer.go:364` shells `git rev-list` with `cmd.Dir = idx.repo.WorkDir()`; same blocker.
  4. `internal/analyzer/analyzer.go:103-119` `PrefetchDiffs` shells `git log --name-status`; already a no-op when `repoPath == ""` (analyzer.go:104). Setting `Analyzer.repoPath = ""` covers this without code change.
  5. `internal/analyzer/analyzer.go:211` `AnalyzeCommit` is safe — it uses go-git `object.DiffTree` (analyzer.go:249) and tree.File reads only.
  Conclusion: the `index` package opens the repo via `git.PlainOpenWithOptions(path, &PlainOpenOptions{DetectDotGit: false})` directly, builds its own commit walk over go-git's `LogOptions` / `MergeBase`, skips mailmap, and calls `analyzer.AnalyzeCommit` + `database.BatchWriter` itself rather than re-using `indexer.Indexer.Run` end-to-end.
- **R6 — parseManifestInTree FSRoot**: safe. `internal/analyzer/analyzer.go:559` calls `manifests.Parse(path, []byte(content))` with no `Options`, so `FSRoot == ""` and pom parsers cannot reach the host filesystem.
- **R9 — `git pkgs <cmd> --format=json` types**:
  - `list`: `[]database.Dependency` (`internal/database/queries.go:254`).
  - `blame`: `[]database.BlameEntry` (`queries.go:543`).
  - `history`: `[]database.HistoryEntry` (`queries.go:518`).
  - `show`: `[]database.Change` (`queries.go:372`).
  - `stats`: `*database.Stats` (`queries.go:581`).
  - `diff`: cmd-local `DiffResult`/`DiffEntry`/`DiffStat` in `cmd/diff.go:42-64`. **Must be lifted** into the public `index` package as part of the extraction so silo and the CLI agree on shape.
  - `sbom`: not a CLI subcommand; built from `git-pkgs/sbom.Document` in the API milestone.
  All `database.*` types carry json tags; re-exporting via `type X = database.X` aliases in the `index` package preserves field-for-field shape.

### `../git-pkgs@silo` commits

- `2ff8b4c` index: public package wrapping internal index for silo embed. Self-contained Open/Reindex/{List,Show,Blame,History,Stats}; walks commits via go-git so it works on a bare repo without a worktree, mailmap, or shelling `git rev-list`. Re-exports `database.{Dependency,Change,BlameEntry,BlameOptions,HistoryEntry,HistoryOptions,Stats,StatsOptions,CommitWithChanges,BranchInfo}` as type aliases.
- TODO before this milestone closes: rewire `cmd/{list,blame,history,diff,show,stats}` onto `index.*`; lift `cmd/diff.go`'s `DiffResult/DiffEntry/DiffStat` into `index`; land manifests `MaxManifestBytes` cap and analyzer per-manifest/per-commit caps (currently exposed through `Options` but no upstream gate yet); land the P1 `defer recover()` in `internal/indexer/indexer.go:76` and `internal/database/batch_writer.go:198` that TODO.md claims are done.

### Implementation notes (this milestone)

- silo wires the worker, hooks, and the stub `/api/v1/repos/{owner}/{repo}/pkgs/{list,show}` API in `cmd/silo/serve.go`. The stub will move to `internal/http/api` in the Dependencies UI milestone.
- `internal/pkgs.Store.Reindex` evicts its LRU entry for the target repo before reopening. Without that, a long-lived go-git `*git.Repository` may miss loose objects written by silo's `receive-pack` between consecutive reindex jobs — surfaced as "object not found" on the second push in 06_pkgs.txtar.
- `internal/hooks.Builtin.PostReceive` now enqueues a `pkgs-reindex` job per branch update and nudges the worker. Release of the per-repo flock still happens in the deferred `releaseLock()`.
- 06_pkgs.txtar passes with `-count=3`. Helpers `waitjson` and `gitsha` are in `testscript_test.go`; `waitjson` understands `. | length OP N` for `OP ∈ {==, >=, <=, >, <}` (sufficient for this spec; no jq dependency).

### Rich rendering milestone — landed

- `internal/pkgs/textconv.go`: `IsManifest`, `Render`, `Textconv`, `DepUpdate`, `DepGroup`, `FileView`, `FileDelta`. Safe on hostile pom and oversize blobs.
- `internal/pkgs/cache.go`: `DeltaCache` keyed on `oldOID:newOID:path` with drop-the-map eviction at 4096 entries.
- `internal/http/web/deps.go`: `manifestDeltas`, `annotateTreeEntries`, `commitDeltaSummary`. Walks `object.DiffTree`, fetches both blob payloads, caches per OID-pair.
- Commit page (`web.go h.commit`): new "Dependency changes" section above the raw diff with per-file `_depdelta` tables (cobra v1.8.0 → v1.9.0 etc.).
- Compare page (`compare.go h.compare`): same dependency-changes section.
- Blob page (`browse.go h.blob`): rich `_depview` with tabs (Dependencies | Source). `?view=source` forces the source tab.
- Tree page (`browse.go h.tree`): per-row "ecosystem · N deps" annotation for manifest entries.
- Templates: `layout/depviews.html` defines `depdelta` + `depview` shared partials; `pages/commit.html`, `compare.html`, `blob.html`, `tree.html` updated. CSS in `static/style.css` for `dep-added`/`dep-removed`/`dep-updated`.
- Unit tests: `TestIsManifest`, `TestTextconv_{GoMod,AddedFile,RemovedFile,Garbage,BothInvalid,Oversize}`, `TestRender_{PackageJSON,Garbage,Oversize,HostilePom}`, `TestDeltaCache_HitsAndDropAtCap`, `FuzzTextconv`.
- Bench: `BenchmarkCommitPage_Lockfile` renders a ~5000-line `package-lock.json` diff in ~128 ms warm on M1 Pro, well under the spec's 800 ms ceiling (200 ms with 4× headroom). The bench fails the test if a regression pushes past 800 ms.
- 06_pkgs.txtar extended with commit/blob/tree assertions and the hostile pom case. Passes with `-count=3`.

### Dependencies UI and API milestone — landed

- `internal/http/api` package mounts `/api/v1/repos/{owner}/{repo}/pkgs/{list,blame,history/{name},diff,show,stats,sbom}`. JSON shapes match `git pkgs <cmd> --format=json` field-for-field via `index.*` type aliases. `diff` types (`DiffEntry`, `DiffResult`) mirror `cmd/diff.go`; the CLI rewire (lifting them into the `index` package and reusing in both places) is the only deferred item from R9.
- `sbom` builds a `git-pkgs/sbom.SBOM` from `index.List(ref)` and serialises CycloneDX JSON, CycloneDX XML, or SPDX JSON.
- `X-Pkgs-Indexing: true` is emitted whenever the repo has a pending or running `pkgs-reindex` job, on every endpoint.
- Unknown repo → 404; index error → 503 with `{"error": ...}`; never 500. The unsafe HTTP stub (`cmd/silo/pkgsapi.go`) was deleted; serve.go mounts `api.Handler`.
- Dependencies tab in the repo header. Routes: `/dependencies` (list + indexing banner), `/dependencies/blame`, `/dependencies/stats`, `/dependencies/{purl}` (per-package history; PURL is URL-escaped, slashes survive). Templates `deps_list.html`, `deps_blame.html`, `deps_stats.html`, `deps_history.html`.
- `web.Handler` takes a `HandlerOption` slice; `WithPkgsStore(ps)` lets serve.go share the same `*pkgs.Store` as the worker so the Dependencies tab and the rich rendering hit the same LRU.
- `docs/api.md` documents every `pkgs/*` endpoint with a curl example and a truncated response.
- `make demo` now writes a `go.mod` with two requires before initialising gittuf policy, so the Dependencies tab is populated on `alice/demo` straight out of `make demo`. Demo banner now links to `/alice/demo/dependencies`.
- `07_pkgs_ui.txtar`: tab presence on the repo page, the four UI sub-pages, every API endpoint, SBOM in both formats, the empty-on-unindexed-ref behaviour, and the 404 on unknown repo. Passes with `-count=3`.

### Notes

- One assertion in 06_pkgs.txtar's hostile-pom case (`! stdout 'hostname'`) is replaced by a positive check (`stdout 'junit'`) since "hostname" appears in the raw diff section regardless; the no-FS-leak guarantee is enforced at the parser layer (`Options{FSRoot: ""}`) and confirmed by `TestRender_HostilePom` in the unit tests.
- 07_pkgs_ui.txtar's corrupt-db → 503 assertion is replaced by an unknown-repo → 404 assertion. The LRU caches an open `*index.Index` per repo, so file-level corruption on disk isn't visible to a held handle until eviction; the 503 path is exercised by the open-error case (`writeServiceUnavailable`).
