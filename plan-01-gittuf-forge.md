# silo first pass: a gittuf-native forge

## Context

Branch protection is a row in someone else's database. gittuf moves it into the repository as a signed reference state log, but today the forge is outside the trust graph: clicking merge on a PR produces a ref update with no RSL entry behind it. The gittuf GitHub App records approvals from the outside; the merge itself still comes from a key nobody delegated to.

silo is a forge that holds a key in each repo's gittuf policy and witnesses an RSL entry for every ref update it accepts, so the web UI and API become participants in the chain rather than a hole in it. A clone from silo gives you refs, the RSL, and the policy; `gittuf verify` on the client checks the chain back to the root keys without trusting silo at all. That's the property no existing forge has.

Small, single binary, pure Go, go-git for plumbing. No issues, no JS build step. Gitea-shaped API where it overlaps so existing tooling can talk to it.

There is no merge button in this pass, and that is a stance, not a scope cut. The motivating problem is "clicking merge produces a ref update with no key behind it"; silo's first answer is the Linux model: push a branch, a holder of an authorising key constructs and pushes the merge from their own machine, silo witnesses it. The web UI's job is to make that local step obvious: the compare page renders the diff, evaluates `VerifyMergeable` to say which policy rule applies and which principals could satisfy it, shows which approvals already sit in `refs/gittuf/attestations`, and prints the exact `git`/`gittuf` commands to run. No PR table, no review threads, no server-held keys; the "PR" is the branch and the review state is signed attestations in the repo. The version that *does* have a merge button (silo constructs the merge commit, hands it to a browser-held key for signing, then accepts and witnesses it) is the progressive enhancement of the same page, in the second-pass plan.

git-pkgs integration (dependency indexing, lockfile diffs, package hosting) is deferred to `plan-02-git-pkgs.md` so the hostile-input hardening it needs can run in parallel rather than gating this.

## The receive path

On push:

1. Unpack objects, compute the proposed ref updates.
2. Load the repo's gittuf policy from `refs/gittuf/policy` and verify the push against it: does the RSL the client sent (if any) chain correctly, are the ref updates authorised by keys the policy permits, do path-scoped rules pass. Run any in-policy Lua hooks.
3. If verification passes, atomically advance the refs *and* append a forge-signed RSL entry to `refs/gittuf/reference-state-log` recording each ref movement, with an annotation naming the authenticated silo user.
4. Fire user exec hooks for CI later.

## Disk layout

```
$SILO_DATA/
  silo.db                   # forge metadata: users, ssh keys, repo list, tokens
  forge.key                 # ed25519 signing key for RSL entries (or agent/sigstore later)
  repos/
    owner/
      name.git/             # bare repo, go-git storage
        HEAD
        config
        objects/
        packed-refs
        refs/
          heads/
          tags/
          gittuf/
            reference-state-log
            policy
            attestations
        silo.lock           # flock target for the receive critical section
        hooks.d/            # user-supplied exec hooks
```

`silo.db` is sqlite (modernc.org/sqlite, no cgo). Schema is deliberately thin: `users`, `ssh_keys`, `tokens`, `repos`, `repo_members`, `jobs`. Everything interesting about a repo lives in the repo.

## Package layout

```
cmd/silo/               # cobra: serve, admin, keygen, pubkey, repo {create,import,init-trust}
internal/
  config/               # SILO_DATA, listen addrs, signer config
  store/                # silo.db access, user/repo CRUD
  gitstore/             # open/create bare repos via go-git, ref txns
  receive/              # owned receive-pack: pkt-line, unpack, callback, ref txn, errors
  gittuf/               # thin wrapper over github.com/gittuf/gittuf/experimental/gittuf:
                        #   LoadRepository, VerifyRef, VerifyMergeable,
                        #   RecordRSLEntryForReference, RecordRSLAnnotation,
                        #   ListRules/ListPrincipals/ListHooks, InvokeHooksForStage.
                        #   isDuplicateEntry + ReconcileLocalRSLWithRemote cover the
                        #   client-already-pushed-RSL case.
  signer/               # adapts gittuf's LoadSigner to silo config; ed25519 file in
                        #   this pass, agent/sigstore later
  hooks/                # ordered chain: builtin gittuf-verify/gittuf-sign then
                        #   exec hooks.d/*; verdict threaded through env
  jobs/                 # durable background queue in silo.db: repack, bundle-regen
  cache/                # content-addressed disk cache keyed on (kind, oid...)
  ssh/                  # gliderlabs/ssh server, key -> user lookup, hands conn to receive/
  http/
    git/                # smart+dumb HTTP: info/refs, git-upload-pack, bundles
    api/                # /api/v1, Gitea-shaped where it overlaps
    web/                # html/template + embed.FS, no JS build
  auth/                 # token + ssh-key auth; user identity threaded into RSL annotation
```

Direct deps beyond stdlib: `go-git/go-git/v5`, `gittuf/gittuf/experimental/gittuf`, `modernc.org/sqlite`, `gliderlabs/ssh`, `spf13/cobra`, `git-pkgs/markup` for blob/README rendering, `git-pkgs/changelog` for tag-page CHANGELOG excerpts.

## gittuf integration

Repo creation (`silo repo create owner/name` or `POST /api/v1/user/repos`) does more than `git init --bare`:

- Generates the bare repo.
- Writes an *unsigned* policy skeleton to `refs/gittuf/policy-staging` naming the creating user's registered key as root and as authorising principal on `refs/heads/*` and `refs/tags/*`, and listing the forge key as a witness role.
- Refuses all pushes with a clear error until the owner has pushed root metadata signed by their own key (which they do with `gittuf trust init` locally, or `silo repo init-trust` which wraps it). Only then does silo write the genesis RSL entry, signed by the forge key as witness.

silo does not sign the root. If it did, a verifier walking back from any commit would bottom out at a root that says "trust andrew's key" with the only authority for that statement being silo itself, which is the trust-the-forge problem moved down a level. Making the owner sign genesis means `gittuf verify` on a clone terminates at a key the owner controls, and silo's compromise can't retroactively change who that was. The first-run UX cost is one extra command.

The witness/authoriser split is the load-bearing default. If the forge key were sufficient on its own, stealing it would let an attacker produce RSL entries that verify cleanly on every repo, which is worse than compromising a stock forge because the signature lends false confidence. With forge-as-witness, a plain `git push` to main from a non-gittuf client fails policy, which is the honest position. Repos that want GitHub-equivalent convenience can add the forge key to their authorising set explicitly; that's their call to make and it's recorded in signed policy.

Importing a repo with existing history works the same way: the owner signs root, silo writes one genesis RSL entry pointing at the imported tips. Commits before that entry are unverifiable by construction; the policy page and `gittuf verify` say so rather than pretending otherwise.

If the client pushed their own RSL entries (gittuf-aware client), silo verifies them, accepts them, and appends a witness annotation rather than a second authorising entry, so there's one record per ref move. If the client didn't (plain git push), silo's entry is the only record and policy decides whether that's sufficient.

The verify→update-refs→append-RSL sequence runs under a per-repo lock. The RSL is a single hash chain; two concurrent pushes to different branches would otherwise both extend the same head and fork it, which a verifying client rejects. The lock is `flock()` on `$repo.git/silo.lock`, not an in-process mutex, because the read-replica story below puts multiple silo processes on the same `repos/` directory and an in-process mutex would let two of them fork the chain. In practice writes route to a single instance (SSH terminates on one host; HTTP is anon-read-only and never reaches this path), so the file lock is belt-and-braces. If the storage backend ever becomes a network filesystem, `flock` semantics there need checking before trusting it; the safe deployment is one writer.

## Security model

What it buys: policy and RSL are in the repo, so a client verifies a clone without trusting silo. Forge compromise is detectable rather than invisible; an attacker on the box can serve refs and write RSL entries, but a verifier walks the chain and stops at the last entry satisfying policy. Policy changes verify against the current root, so an admin can't silently widen rules. Pure Go keeps every parser memory-safe.

The forge key is the high-value target. `internal/signer` is an interface with three backends: `ed25519` (file at rest, the default and the air-gapped fallback; this pass ships only this one), `agent` (ssh-agent or PKCS#11, key never on disk), and `sigstore` (keyless: silo authenticates to Fulcio as its own OIDC identity, gets a ten-minute cert, signs, and the entry lands in Rekor). The Sigstore backend removes the long-lived secret entirely and puts every forge-signed RSL entry in a transparency log silo doesn't control, so a repo owner can monitor Rekor for entries from silo's identity they didn't cause and catch a compromised forge from outside. `silo pubkey` prints the ed25519 key or the Sigstore identity string for inclusion in policies; rotation is a policy update signed by the existing root.

Forge key rotation is awkward if the key is embedded by value in every repo's policy: changing it needs each repo's root holders to re-sign. For witness-only repos that's tolerable (old entries verify against the old key, new against the new, and witness annotations aren't policy-required so nothing breaks in the gap). For repos that opted the forge into the authorising set, pushes fail until the owner re-signs. Check whether gittuf's delegation model lets the forge be referenced as a named principal whose key material is resolved from a metadata blob silo *can* update under its own signature, rather than copied into every root. If not, the operational answer is "rotate rarely, announce loudly, and the witness-only default keeps the blast radius small".

Hostile input in this pass is pushed packfiles and pushed RSL/policy blobs handed to gittuf's verifier. silo wraps verification in `recover()` with a context deadline and caps incoming pack size and object count. The owned receive-pack is the right place to fuzz: pkt-line, packfile, policy blob.

Transport key and signing key should be separable. A user can register one SSH key for `git push` auth and a different key in the gittuf policy; the RSL annotation records both ("silo authenticated user X via transport key Y; ref update authorised by policy key Z") so the audit trail distinguishes them.

Exec hooks run with `SILO_REPO` and `SILO_PUSHER` in env, under a timeout, as the silo user. They live outside the object store so a push can't place them.

## Performance

Read traffic dominates on a public instance, on the order of 1000× more fetch than push, and almost all of it is anonymous: CI runners, `go get`, cargo git deps, actions checkout. Those clients do full or shallow clones of a fixed ref, which means their requests are byte-identical between pushes. The design target is that go-git's packer runs once per ref-state, not once per request.

Layers, in the order a request hits them:

- A caching reverse proxy or CDN in front. `info/refs` carries `ETag: <refStateHash>`; dumb-protocol objects and bundle files are immutable with year-long `Cache-Control`. Most repeat anon traffic stops here.
- bundle-uri advertised in the v2 capability line, pointing at `/bundles/<refStateHash>.bundle`. Clients that support it pull the static bundle and then issue a tiny incremental fetch. The bundle is regenerated by `internal/jobs` after each push.
- Smart upload-pack response cache keyed on `(refStateHash, sha256(requestBody))`. Full clones and `--depth 1` clones from clients without bundle-uri produce identical POST bodies until a ref moves; second request onwards is a disk read.
- Dumb HTTP for everything else, served off disk. A periodic repack job keeps each repo close to one packfile so this stays efficient.
- go-git's packer, reached only by incremental fetches with a unique have-set. That's the developer-`git pull` case, which is the low-volume tail. A per-IP token bucket on this path bounds the worst case.

Anon reads never write, so horizontal scale is N read-only silo processes behind a load balancer all serving the same `repos/` (shared filesystem or rsync'd), with the SSH listener and the job worker running on exactly one writer instance. RSL and policy are ordinary refs and replicate with everything else.

Shallow and partial clone are where go-git's server support is thinnest; CI doing `--depth 1` against a big repo is the case to test first. If the packer is still the bottleneck after the caches, the escape hatch is exec'ing `git-upload-pack` for that one endpoint while keeping receive and gittuf in-process.

Web reads cache cleanly. Commit diff, RSL table, and policy graph are pure functions of OIDs that never change. `internal/cache` is a content-addressed disk cache keyed on `(kind, oid...)`; first render computes, subsequent renders read. The commits-page history walk is the one read that doesn't cache; go-git handles it adequately on packed repos.

Push is heavier than a stock forge but not hot. Ed25519 verify ≈ 50µs and sign ≈ 25µs; the cost is the RSL walk (O(new entries) incrementally) and the per-repo lock.

Background work (repack, bundle regeneration) needs to survive restarts. A `jobs` table in `silo.db` (id, repo, kind, state, payload, attempts) polled by a worker goroutine is enough; in-memory channels alone drop work on deploy.

## Web UI

Server-rendered `html/template`, CSS in one file, no JS framework. Pages:

```
/                                  # repo list
/:owner/:repo                      # readme + tree at default branch + RSL status badge
/:owner/:repo/tree/:ref/*path      # tree
/:owner/:repo/blob/:ref/*path      # blob, rendered via git-pkgs/markup
/:owner/:repo/commits/:ref         # log
/:owner/:repo/commit/:sha          # diff + "RSL entry abc123 signed by forge,@andrew"
/:owner/:repo/branches             # ahead/behind, last RSL mover, mergeable badge
/:owner/:repo/compare/:a...:b      # diff + merge guide (see below)
/:owner/:repo/tags                 # tags, each with "policy: 2-of-3 release keys ✓",
                                   #   CHANGELOG excerpt via git-pkgs/changelog
/:owner/:repo/refs/gittuf          # RSL viewer: the signed log as a table
/:owner/:repo/policy               # rendered gittuf policy: who can push what
```

The RSL viewer and policy page are the bits no other forge has. The policy page is "branch protection settings" rendered from signed metadata in the repo, not a settings table. Path-scoped `file:` rules render as a CODEOWNERS-shaped table; gittuf does not read the CODEOWNERS file, but `silo repo import` reads an existing `.github/CODEOWNERS` to draft a delegation for the owner to sign.

## Branches and the merge guide

There is no pull-request model. A branch awaiting merge is a branch in `refs/heads/*` whose tip isn't reachable from the default branch; review state is reference-authorisation attestations in `refs/gittuf/attestations`. The UI is a read-only lens over that.

`/:owner/:repo/branches` lists every branch with ahead/behind counts against the default, the last RSL entry that moved it (who, when, which key), and a `VerifyMergeable(default, branch)` badge: mergeable now, needs N more approvals, or blocked by rule X. No server-side table backs this; it's computed from refs on render and cached by `(defaultTip, branchTip)`.

`/:owner/:repo/compare/:base...:head` is the merge guide. It renders:

- The combined diff.
- The policy verdict from `VerifyMergeable`: which rule governs advancing `base` with these changes, the required threshold, and which principals can satisfy it.
- Approvals collected so far, read from `refs/gittuf/attestations` via gittuf's reference-authorisation index: "1 of 2 — alice (key `SHA256:abc…`, 2026-06-14)". Each is a signed in-toto statement in the repo, so this view is identical from any clone.
- A copy block of the commands a reviewer runs locally to add an approval without merging:
  ```
  git fetch origin refs/gittuf/*:refs/gittuf/*
  gittuf attest authorize --base main --head feature-x
  git push origin refs/gittuf/attestations
  ```
- A copy block of the commands an authorising principal runs locally to perform the merge:
  ```
  git fetch origin
  git checkout main && git merge --no-ff origin/feature-x
  gittuf rsl record main
  git push origin main refs/gittuf/reference-state-log
  ```
  with a one-line variant for users who have `git-remote-gittuf` installed (the remote helper handles RSL recording on push).
- If the viewer is logged in and one of their registered keys is in the principal set, the page says so; otherwise it names who could act.

silo's pre-receive accepts the resulting push because the merger's RSL signature plus the collected attestations meet the threshold; silo appends its witness annotation and the branch shows as merged on the next render. Nothing about this flow stores state on the server that isn't a git ref, and no key capable of authorising the merge ever leaves the reviewer's machine.

## HTTP API

Mount under `/api/v1` and match Gitea's shapes for the subset we implement, so `tea`, terraform providers, etc. have a chance of working:

```
GET    /api/v1/version
GET    /api/v1/repos/:o/:r
POST   /api/v1/user/repos
GET    /api/v1/repos/:o/:r/contents/*path
GET    /api/v1/repos/:o/:r/git/commits/:sha
GET    /api/v1/repos/:o/:r/git/refs(/:ref)
GET    /api/v1/repos/:o/:r/branches
GET    /api/v1/repos/:o/:r/tags
GET    /api/v1/repos/:o/:r/raw/*path
GET    /api/v1/user            /users/:name        /user/keys
```

silo extensions:

```
GET    /api/v1/repos/:o/:r/gittuf/rsl            # RSL entries as JSON
GET    /api/v1/repos/:o/:r/gittuf/policy         # effective policy
GET    /api/v1/repos/:o/:r/gittuf/verify/:ref    # is this ref tip backed by a valid RSL chain?
```

Auth is bearer token (`Authorization: token ...`) matching Gitea, mapped to a silo user. Tokens are read-only on git refs (no push capability) but can create repos and manage keys.

Because the overlap is Gitea-shaped, `github.com/git-pkgs/forge` works as silo's CLI client out of the box via its Gitea backend: `forge --host silo.example.com repo view`, `forge branch list`. A `silo` backend in that library later covers the `/gittuf/*` extensions, giving `forge rsl log`, `forge policy show`. That's also the cheapest conformance test for the API surface.

## Hooks

There are two layers, with different trust properties.

In-policy Lua hooks are gittuf's own. The repo's signed policy can carry content-addressed Lua scripts at `HookStagePreCommit` / `HookStagePrePush`, executed in `internal/luasandbox` with a git-blob-read API and nothing else: no network, no exec, deterministic. silo runs them via `InvokeHooksForStage` during pre-receive. Because they're in the policy, the same check runs on every verifying client, not just on silo. (Dependency-aware Lua hooks need data silo doesn't write yet; that's in the second-pass plan.)

Forge-side hooks are silo's. `internal/receive` computes a `Verdict{Ref, Rule, Principals, ThresholdMet}` per ref update from `VerifyRef` and threads it through:

- `pre-receive`: gittuf verification + in-policy Lua hooks (cannot be disabled), then `hooks.d/pre-receive.*` exec'd with git's standard stdin (`old new ref` lines) and `SILO_GITTUF_RULE` / `SILO_GITTUF_PRINCIPALS` / `SILO_GITTUF_THRESHOLD` in env. Non-zero exit rejects the push.
- `post-receive`: gittuf RSL sign, then `hooks.d/post-receive.*` with the same verdict env.

Exec hooks get `SILO_REPO`, `SILO_PUSHER`, `SILO_DATA` plus the verdict variables. CI plugs in here later.

## Transports

The two transports are split by capability, not preference. HTTP is anonymous read only; SSH is the only authenticated git transport. API tokens exist for `/api/v1` and cannot push. There is no bearer credential anywhere in the system that can move a ref, which removes the leaked-PAT class of compromise and means every push arrives with a pubkey that can be named in the RSL annotation.

HTTP, public repos only:

- `GET /:owner/:repo.git/info/refs?service=git-upload-pack` — smart ref advertisement, includes `bundle-uri` capability pointing at the current bundle.
- `POST /:owner/:repo.git/git-upload-pack` — smart fetch. Response is looked up in `internal/cache` by `(refStateHash, sha256(requestBody))` before falling through to go-git's packer.
- `GET /:owner/:repo.git/objects/...`, `info/packs`, `HEAD`, `packed-refs` — dumb protocol, served straight off disk with long cache headers.
- `GET /:owner/:repo.git/bundles/<refStateHash>.bundle` — static clone bundle, regenerated by a job after push.

All of the above is safe behind a caching reverse proxy or CDN. `info/refs` carries `ETag: <refStateHash>`; objects and bundles are immutable.

SSH via `gliderlabs/ssh`: the public-key callback looks up the key in `silo.db` to get the user, then the exec handler matches `git-upload-pack '/owner/repo.git'` / `git-receive-pack ...` and hands the session to the same go-git transport with the resolved user attached. Private-repo fetch and all pushes come through here. The SSH listener only needs to scale to contributor count.

`refStateHash` is the sha256 of the sorted `refname\x00oid` list and is recomputed when refs change; it keys the upload-pack cache, the bundle filename, and the `info/refs` ETag, so one push invalidates everything that should change and nothing else.

## Modules to extract

Pieces of this pass that are generic enough to live as standalone `git-pkgs/*` libraries.

`git-pkgs/gitserve` — go-git's `transport/server` wired to `net/http` and `gliderlabs/ssh` behind a `Loader` interface plus pre/post-receive callbacks. Every small Go forge hand-rolls this. The bundle-uri advertisement, `refStateHash` ETag, dumb-protocol static handler, and upload-pack response cache are generic to any go-git host and live here, not in silo proper. Biggest extraction, most likely to get outside adoption.

`git-pkgs/codeowners` — parser for GitHub/GitLab/Gitea CODEOWNERS dialects → `[]{Pattern, Owners}`, plus `ToGittufRules(principals map[string]tuf.Principal)` that drafts `file:` delegations. silo's import flow and any standalone gittuf onboarding tool.

`git-pkgs/oidcache` — disk memoisation of `func(oids ...Hash) []byte` keyed on `(kind, oid...)`. silo's diff/RSL-table renders, proxy's version-diff UI, any go-git tool that renders from immutable objects. Same fifty lines everywhere.

Not extracted: the `Verdict` struct and matcher belong upstream in gittuf; the sqlite job queue is too generic to be a `git-pkgs` thing.

## Build order

0. Spike: own receive-pack. Read pkt-line ref-update commands and the packfile off the wire, unpack objects into go-git storage, hold the proposed updates in memory, run a callback, apply refs on success. Do not rely on go-git's `transport/server` having a clean intercept point; assume it doesn't and treat any later discovery that it does as a simplification. Week-one work because the whole architecture sits on it. Becomes the core of `git-pkgs/gitserve`.
1. `cmd/silo serve` + `internal/gitstore` + smart HTTP upload-pack. Can clone a manually-placed bare repo.
2. `internal/store` (silo.db), `silo admin user create`, SSH transport with pubkey auth, the owned receive-pack with no verification. Can push.
3. `internal/signer` (ed25519-file only) + `internal/gittuf`: repo create writes the unsigned policy skeleton; owner signs root; receive verifies under per-repo `flock`; post-receive witnesses via signer. `silo keygen`, `silo pubkey`. Push-rejection error rendering (`internal/receive/errors.go`).
4. Web: repo list, tree/blob (via `git-pkgs/markup`), commit, log, tags. RSL viewer at `/:o/:r/refs/gittuf` and policy page at `/:o/:r/policy` from `ListRules`/`ListPrincipals`. Branches page and compare/merge-guide page from `VerifyMergeable` + the attestations index.
5. `/api/v1`: Gitea-shaped read endpoints + `/gittuf/{rsl,policy,verify}`. Verify by pointing `git-pkgs/forge --host` at it.
6. Exec `hooks.d/*` with verdict env (`SILO_GITTUF_RULE`/`_PRINCIPALS`/`_THRESHOLD`).
7. `internal/jobs` + `internal/cache` (`git-pkgs/oidcache`). Anon-read scale: dumb HTTP, `refStateHash` ETag, upload-pack response cache, bundle-uri + regeneration job, periodic repack, per-IP token bucket.
8. Fuzz harness for `internal/receive` (pkt-line, packfile, policy blob).

End state: `git clone`, `git push`, policy enforced, RSL signed and browsable, `gittuf verify` on a clone passes without trusting silo.

## Verified

- gittuf importable surface: `github.com/gittuf/gittuf/experimental/gittuf` with `LoadRepository`, `VerifyRef`, `VerifyMergeable`, `RecordRSLEntryForReference`, `RecordRSLAnnotation`, `ListRules`, `ListPrincipals`, `ListHooks`, `InvokeHooksForStage`, `LoadSigner`. Sigstore principals supported via `internal/signerverifier/sigstore`. RSL dedup via `isDuplicateEntry`.
- gittuf does not read CODEOWNERS; path rules are `file:glob` patterns in policy.
- gittuf carries Lua hooks in policy (`HookStagePreCommit`/`PrePush`, `luasandbox`).

## To verify before coding

- gittuf's `experimental/gittuf` operates on `pkg/gitinterface.Repository`. Confirm it opens a bare repo and `VerifyRef`/`RecordRSLEntryForReference` work without a worktree.
- Whether gittuf delegations can reference the forge as a named principal whose key is resolved from forge-signed metadata, rather than embedded by value in every repo's root. Determines how painful key rotation is.
- go-git protocol v2 server support, specifically whether we can inject a `bundle-uri` capability. If absent, fall back to `Link: <...>; rel="bundle-uri"` on the info/refs response.
- go-git upload-pack handling of `--depth` and `filter=` from the server side.

## Resolved decisions

- Licence: MIT. proxy stays a separate process over HTTP in pass 2.
- Push-rejection error surface: multi-line over sideband band 2 (ref / rule + threshold + principals / pusher identity / approvals count / policy URL), then report-status `ng <ref> policy`. Exact format and golden test in `SPEC.md`.

## Verification

- `go test ./...` with `testscript` cases: create repo, owner signs root, push with valid signer, push with revoked key (rejected), push to a ref the policy doesn't permit (rejected with the right sideband error), concurrent pushes to two branches (RSL stays linear).
- `golangci-lint run --enable gocritic,gocognit,gocyclo,maintidx,dupl,mnd,unparam,ireturn,goconst,errcheck ./... && govulncheck ./... && deadcode ./...`
- Manual: `silo serve`, `gittuf trust init` locally, push, `git clone http://localhost:8080/andrew/demo.git` from a second machine, `gittuf verify` passes.
