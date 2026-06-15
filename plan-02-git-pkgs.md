# silo second pass: git-pkgs and package hosting

## Context

git-pkgs turns lockfile history into a queryable database, but forges treat lockfiles as binary blobs and hide the diffs. The Forgejo prototype showed dependency data can live in the UI; bolting it on means a second indexing pass and a schema the forge doesn't own. silo runs git-pkgs indexing in the receive path, so the commit page for a lockfile change shows `+kamal 1.0.0 / puma 5.0.0 -> 6.0.0` instead of three hundred lines of diff, and every repo gets a Dependencies tab backed by the same sqlite schema git-pkgs uses locally.

This pass also makes silo a package host. A tagged release whose RSL entry satisfies the repo's release rule is published into a co-located `git-pkgs/proxy` instance with an in-toto provenance attestation naming the RSL entry, so the chain runs registry artifact → attestation → silo tag → RSL → policy keys with no gap.

The thread back to the first pass is dependency-aware policy: silo writes a snapshot of the dependency set to a git ref so gittuf's in-policy Lua hooks can gate "adding a new direct runtime dependency requires threshold 2" or "no GPL in the transitive set" deterministically, and the same check runs on every verifying client.

This pass starts after, or alongside once unblocked by, the upstream hardening in `~/code/git-pkgs/git-pkgs/TODO.md`. The first-pass forge (`plan-01-gittuf-forge.md`) does not depend on any of it.

## git-pkgs modules and where they sit

silo is mostly glue over libraries that already exist in `github.com/git-pkgs/*`.

In the receive/index path, running on pushed (hostile) blobs:

- `manifests` — `Parse(filename, content) -> Result{Ecosystem, Kind, Dependencies}`. The reindex hook walks new commits, finds changed manifest paths, calls this, writes rows.
- `gitignore` — wildmatch filter during the tree walk so vendored/`node_modules` manifests are skipped.
- `purl`, `vers` — primitives for identity and range comparison; called in tight loops during indexing so they're on the hostile-input audit list.

In the read path, UI and API:

- git-pkgs' own db layer — the sqlite schema and query funcs behind list/blame/history/diff/show/stats. Extracted as `git-pkgs/git-pkgs/index` (roadmap issue #116).
- `sbom` — `Document`/`Package` model, writes CycloneDX or SPDX JSON/XML. Tag page export.
- `cooldown` — `Config.IsAllowed(ecosystem, purl, publishedAt)`. Commit pages flag any newly-added dep whose version is still inside the window.
- `spdx` — normalise registry licence strings before rendering.
- `changelog` — already used in pass 1 for the repo's own CHANGELOG; here also for upstream changelog excerpts on dependency history pages.
- `archives` — read ZIP/TAR/gem in memory. Release-asset browsing on tag pages, and the source browser for artifacts published into proxy.
- `reuse` — REUSE.toml/headers → per-file licence on blob pages.

For provenance:

- `attestation` — stdlib-only parser for sigstore bundles → SLSA provenance fields. UI uses it to render attestations on tag pages and on dependency rows.
- `sigstore` — `VerifyBundle(ctx, bundle, alg, digest)` against the Sigstore TUF trust root. Tag-page badge, and the verify step when proxy serves a first-party artifact.

Via the co-located proxy, never called from silo's process directly:

- `enrichment` — `BulkLookup`/`GetVersion` over ecosyste.ms / deps.dev / direct registries. proxy exposes this as `/api/bulk` and `/api/package/...`.
- `vulns` — OSV query by PURL. proxy exposes `/api/vulns/...`.
- `registries` — metadata and `fetch/` for streaming artifact downloads. proxy uses it for upstream; the `pkgs-publish` hook uses its knowledge of per-ecosystem artifact layout when writing first-party packages into proxy storage.

Not used server-side (CLI-dependent): `managers`, `resolve`. These matter later if silo grows CI runners.

## Upstream prerequisites

`~/code/git-pkgs/git-pkgs/TODO.md` (audit 2026-06-15) catalogues the hostile-input findings. The ones that gate this pass:

- P0: maven `pom.xml` follows `<parent><relativePath>` onto the host filesystem (`manifests/internal/maven/maven.go:43`). On silo a pushed pom can read `$SILO_DATA/forge.key`. Fix: nil/noop fetcher when `Parse` is called with a synthetic filename.
- P1: two unrecovered goroutines in `internal/indexer/indexer.go:76` and `internal/database/batch_writer.go:198` would take silo's process down on a go-git or sqlite panic.
- P2: no global manifest byte cap (`manifests.go:51`), no per-manifest dep cap or per-commit manifest cap (`internal/analyzer/analyzer.go`), batch flush keyed only on commit count (`batch_writer.go:180`), unbounded commit messages. A single push can allocate hundreds of MB and write millions of rows.

Nice to have before, fine to land alongside:

- P3: `vers` bubble sort (`parser.go:139`) and quadratic `Intersect`; npm/maven recursive parsers with O(n²) append.
- P4: chunk `ensureManifests` by SQLite variable limit; bound TEXT columns at write; stream rev-list.

Verified safe in the same audit: encoding/xml has no XXE path, yaml.v3 rejects alias bombs, all regex is RE2-linear, zero `panic`/`log.Fatal` in library code.

Separately: extract `github.com/git-pkgs/git-pkgs/index` from `internal/{analyzer,indexer,database}` exposing `Open(gitDir) (*Index, error)`, `(*Index).Reindex(repo, oldTip, newTip)`, and `List`/`Blame`/`History`/`Diff`/`Show`/`Stats`. This is roadmap issue #116.

## Integration

`internal/pkgs` opens the per-repo `pkgs.sqlite3` (sits inside `$repo.git/`) via the `index` package. Post-receive enqueues `Reindex(old, new)` for each updated branch as a background job; first push of imported history triggers a full walk and the UI shows "indexing…" until it completes. silo wraps the call in `recover()` with a context deadline and treats failure as non-fatal so a bad lockfile can't block pushes.

UI/API additions to the first-pass surface:

- Commit page: if the commit touched any manifest path, render `index.Show(sha)` inline above the file diffs. Lockfile diffs render through `manifests.Parse` on both sides (the textconv) instead of raw, cached by `(oldOID, newOID)` in `oidcache`.
- Compare page: dependency diff section from `index.Diff(a, b)`.
- `/:owner/:repo/dependencies`: `list`, `blame`, `stats` tabs.
- `/:owner/:repo/dependencies/:purl`: `history` for one package, with upstream changelog excerpts.
- Tag page: `sbom` export (CycloneDX/SPDX) on demand from the index at that ref; release-artifact browser via `archives`.
- `/api/v1/repos/:o/:r/pkgs/{list,blame,history,diff,show,sbom,vulns}` returning the same JSON shapes as `git pkgs ... --format=json`.

Per-repo sqlite handles sit in an LRU of open `*sql.DB`, WAL mode so reindex doesn't block readers, and a corrupt db renders "dependencies unavailable" rather than a 500.

## Dependency-aware policy hooks

`refs/git-pkgs/metadata` holds a compact JSON snapshot (`[{purl, scope, direct, integrity}]` per manifest) as of the last accepted push, written post-receive via the `git-pkgs/depref` module. During pre-receive, after objects are unpacked but before refs move, silo computes the *proposed* snapshot from the incoming tree via `manifests.Parse` (stateless, no sqlite) and writes it to `refs/git-pkgs/metadata-incoming`. A Lua hook in the repo's gittuf policy reads both via `gitReadBlob`, diffs them with `depref.Diff`, and can express "new direct runtime dependency requires threshold 2" or "no purl with licence category copyleft" against what this push actually changes. On accept, post-receive fast-forwards `metadata` to `metadata-incoming` and drops the incoming ref; on reject, the incoming ref is discarded.

Because the hook is in the signed policy and reads only repo-local refs, the same check runs on every verifying client, not just on silo. A hook that needs network (OSV lookup, registry check) runs as a forge-side `hooks.d/` exec instead and writes an attestation back via `AddReferenceAuthorization`.

## proxy as sibling

`github.com/git-pkgs/proxy` runs alongside silo as a separate process (it's GPL-3, so linking depends on silo's licence; HTTP between them avoids the question). It serves two roles.

As enrichment backend: silo's Dependencies tab and the `/api/v1/.../pkgs/{vulns,outdated,licenses}` endpoints call proxy's `/api/*` instead of upstream registries. proxy already caches to disk/S3, has a circuit breaker on upstream, and exposes `/api/bulk`, `/api/vulns/{eco}/{name}/{ver}`, and `/api/outdated`. silo's config carries a `proxy.url`; when set, `internal/pkgs` uses it as the enrichment backend and the request-path cost is a local HTTP call to a warm cache. When unset, those columns render empty rather than calling out. proxy's cooldown setting also feeds the UI: a dependency whose pinned version is still inside the quarantine window gets flagged on the commit page that introduced it.

As package host: a built-in post-receive hook, `pkgs-publish`, fires on tag pushes whose verdict names the repo's configured release rule. For Go it produces the module zip and `.info`/`.mod` directly from the tagged tree; for ecosystems with a build step it defers to a CI hook. Alongside the artifact it emits an in-toto SLSA provenance statement (via `git-pkgs/provenance`) naming the repo, tag, commit, and the OID of the RSL entry that authorised the tag, signed via `internal/signer` (so Sigstore-signed and Rekor-logged when that backend is active). Both are written into proxy's storage with a `repository_url` qualifier pointing back at the silo repo, and proxy serves them on `/go/`, `/npm/`, `/cargo/` etc. alongside cached upstream packages. A consumer verifies: artifact hash → attestation → Rekor inclusion → silo identity, and separately walks the RSL from the named entry back to the policy root. Same statement format as npm provenance and PyPI attestations, so existing verifiers work.

`proxy mirror --sbom` can pre-warm the cache from any repo's `index.SBOM(ref)` output, so a fresh silo+proxy install can be brought to a known-good offline state from a checked-in SBOM. Much of the heavy anonymous read load on a forge that is also a package host is `go get` / `npm install` rather than `git clone`; proxy already serves those as immutable artifacts from disk or S3.

## Modules to extract

`git-pkgs/depref` — the `refs/git-pkgs/metadata` contract. Compact JSON dependency snapshot written to a git ref so gittuf Lua hooks can `gitReadBlob` it. `Write(repo, ref, deps)`, `Read(repo, ref)`, `Diff(a, b)`. silo writes it post-receive, the git-pkgs CLI writes it from a local hook, gittuf policy hooks read it. Approach 2 from `~/code/git-pkgs/gittuf.md` as a stable interchange.

`git-pkgs/provenance` — the produce side that `attestation` (parse) and `sigstore` (verify) are missing. `Build(subjects, source, builder) -> *intoto.Statement`, `Bundle(stmt, signer) -> []byte`. silo's `pkgs-publish` uses it; proxy could attest "fetched from upstream X with digest Y at time T" on every cache fill. Stdlib + sigstore-go only.

## Build order

9. Upstream: P0/P1/P2 from `git-pkgs/git-pkgs/TODO.md`; the `git-pkgs/git-pkgs/index` extraction (issue #116); `git-pkgs/depref` module.
10. `internal/pkgs`: post-receive enqueues reindex + writes `refs/git-pkgs/metadata`; pre-receive computes `metadata-incoming` for Lua hooks. Commit-page textconv, Dependencies tab, `/api/v1/.../pkgs/*`.
11. proxy integration: `proxy.url` config, enrichment via proxy `/api/*`, cooldown flags on commit pages. `git-pkgs/provenance` module. `pkgs-publish` hook for tag → Go module zip into proxy storage with in-toto attestation via `internal/signer`.
12. `signer.type: sigstore` (Fulcio + Rekor) and `signer.type: agent`.
13. Client-signed merge: compare page constructs the merge commit, hands it to the authorising user's local `gittuf` or a browser-held key for signing, accepts and witnesses the signed result. This is the demo that makes the model click.

## To verify before coding

- `sigstore/sigstore-go` signing API: confirm it accepts an arbitrary OIDC token so a self-hosted silo can use its own issuer.
- Exact Lua sandbox API surface (`gitReadBlob` etc.) and whether `InvokeHooksForStage` can be passed extra parameters so the hook receives the `metadata-incoming` ref name without hardcoding it.
- TODO.md follow-up audits 3/4/6/7/8/9 (adversarial history, textconv RSS, query plans on skewed distributions, SSRF via lockfile registry URLs, path traversal sweep, fuzz coverage).

## Verification

- `go test ./...` with `testscript` cases: push a lockfile change and assert `/api/v1/.../pkgs/show/:sha` returns the delta; push a pom with `<relativePath>../../forge.key` and assert reindex refuses it; push a tag satisfying the release rule and assert the artifact + attestation land in proxy storage; push a tag under a weaker rule and assert `pkgs-publish` does not fire.
- Manual: edit `go.mod`, push, open `/andrew/demo/commit/<sha>` and see the dependency delta + RSL entry; `GOPROXY=http://localhost:8081/go go get` the tagged module and verify the attestation with `git-pkgs/sigstore`.
