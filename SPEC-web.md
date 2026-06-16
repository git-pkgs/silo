# silo spec — web UI

Goal: a read-only HTML view of repos and their gittuf state, enough to see what the receive path is doing without reading server logs. Server-rendered `html/template`; styles are plain CSS files so pages paint on first frame from browser cache.

Conventions and quality bar inherited from `SPEC.md`.

## Styling

Two stylesheets, both `<link rel="stylesheet">`, both cacheable:

- `https://cdn.jsdelivr.net/npm/basecoat-css@0.3.11/dist/basecoat.cdn.min.css` — basecoat component classes (`table`, `tabs`, `dropdown-menu`, `btn-*`, `card`) and the shadcn theme variables (`--background`, `--foreground`, `--border`, `--muted-foreground`, …). The CDN build is compiled with `source(none)` so it ships zero Tailwind utility classes; it's components + preflight + theme only.
- `/static/style.css` — silo's layout: `.container`, `.topbar`, `.repo-nav`, `.panel`, `.kv`, `.diff`, `.rsl-pass`/`.rsl-fail`, heading sizes, table padding overrides. Written against basecoat's theme vars so it follows dark mode for free.

Two deferred scripts: `basecoat-css/dist/js/all.min.js` (drives `dropdown-menu` and `tabs`) and `lucide@0.545.0` for icons (`<i data-lucide="…">`, `lucide.createIcons()` on `DOMContentLoaded`).

No JS-generated stylesheets, no Tailwind browser/Play-CDN runtime, no FOUC cloak. If utility classes are ever wanted, compile them to a static file with the standalone Tailwind CLI and serve that; don't ship the runtime.

## Deliverables

`internal/http/web/`:

- `web.go` — `Handler(st *store.Store, gst *gitstore.Store, baseURL string) http.Handler` mounting the routes below. Every repo handler builds a `page{Repo, BaseURL, Active, Ref, Refs}` so the header can render the persistent nav and refs dropdown. `commitDiff(c)` produces a coloured unified diff via go-git's `object.DiffTree(parentTree, commitTree).Patch()`; `rslForCommit` filters `WalkRSL` to entries whose `TargetID` is the commit plus their annotations.
- `templates.go` — `//go:embed templates/**/*.html`; one parsed `*template.Template` per page composed with the layout.
- `static.go` — `//go:embed static`; `StaticHandler()` exported separately because `/{owner}/{repo}` wildcard routes conflict with `/static/` under ServeMux specificity.
- `templates/layout/{base,header,footer}.html` — `base` defines `<head>` and the container; `header` renders the brand row plus, when `.Repo` is set, the repo nav (name, refs `dropdown-menu`, `tabs` tablist of overview/rsl/policy/log links with `aria-selected` driven by `.Active`).
- `templates/pages/{index,repo,log,commit,rsl,policy}.html`.
- `static/style.css`.

`cmd/silo/serve.go`: top-level dispatcher routes `/static/` → `web.StaticHandler()`, `*.git/{info/refs,git-upload-pack,git-receive-pack}` → git transport, everything else → web.

`internal/gittuf/rsl.go`:

- `WalkRSL(ctx, repo) ([]RSLEntry, error)` — walk `refs/gittuf/reference-state-log` parents newest-first, parsing each commit message into `RSLEntry{ID, Kind, Number, Ref, TargetID, Message, AnnotatesID, SignerKeyID, Timestamp}`. `SignerKeyID` is the SSH SHA256 fingerprint pulled from the commit's `SSH SIGNATURE` block.
- `(*Repo).Policy(ctx) (*PolicySummary, error)` — `{Rules []Rule, Principals map[string][]string}` from `ListRules` + `ListPrincipals`.
- `SignerFingerprint(sig string) string` — exported for the commit page's "signed by" row.

## Routes

```
GET /                          repo list: owner/name, ref count, last RSL entry time
GET /:owner/:repo              refs table + README card
GET /:owner/:repo/log/:ref     commit list: sha, author, date, subject; ?after=<sha>
GET /:owner/:repo/commit/:sha  metadata, signer fingerprint, parents,
                               RSL entries whose target is this sha (with witness
                               annotations, row tinted on VerifyRef result),
                               changed files, unified diff
GET /:owner/:repo/rsl          WalkRSL as a table: #, kind, ref/annotates,
                               target/message, signer fingerprint, age; rows for
                               non-gittuf refs tinted by VerifyRef(ref)
GET /:owner/:repo/policy       Rules table (name, patterns, threshold, principals)
                               + Principals table (id, key fingerprints)
GET /static/style.css          embedded
```

Unknown repo → 404.

## Acceptance

`testdata/testscript/04_web.txtar`: seed alice/demo with policy and a push (same flow as 03), then `curl -fsS` each route and assert content; the commit page asserts `Reference state log` and a `class="diff"` block; static asserts `rsl-pass`; layout asserts `basecoat-css`, `role="tablist"`, `class="dropdown-menu"`.

Unit: `TestWalkRSL` (ordering, field extraction, annotation message PEM-decode), `TestSignerFingerprint`, `TestHandler_Routes` (every route 200 `text/html`, unknowns 404), `TestStaticHandler`, `TestLoadTemplates`.

`scripts/demo.sh` (run via `make demo`) builds silo + gittuf, generates an alice keypair, seeds `alice/demo` with a policy and two pushes (fetching the witness annotation between them so the RSL stays fast-forward), and leaves `silo serve` running on `:8080` for manual clicking.

**Done when:** `04_web.txtar` passes and `make demo` brings up a server where `/alice/demo/rsl` shows alice's entries with her fingerprint and green rows, the tabs and refs dropdown work on every repo page, and navigating between tabs paints without a flash.
