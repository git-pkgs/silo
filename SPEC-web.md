# silo spec — web UI

Goal: a read-only HTML view of repos and their gittuf state, enough to see what the receive path is doing without reading server logs. Server-rendered `html/template`, one CSS file, no JavaScript.

Conventions and quality bar inherited from `SPEC.md`.

## Deliverables

`internal/http/web/`:

- `web.go` — `Handler(st *store.Store, gst *gitstore.Store, baseURL string) http.Handler` mounting the routes below.
- `templates/*.tmpl` — one base layout, one template per page, embedded via `embed.FS`.
- `static/style.css` — embedded; monospace, table-heavy, no framework.

`internal/gittuf/` additions:

- `(*Repo).WalkRSL(ctx) ([]RSLEntry, error)` — walk `refs/gittuf/reference-state-log` parents; each `RSLEntry{ID, Number, Ref, TargetID, SignerKeyID, Timestamp}`. Signer key ID comes from the commit signature (parse the SSH sig header to a fingerprint, or empty if unsigned).
- `(*Repo).Policy(ctx) (*PolicySummary, error)` — `{Rules []Rule, Principals map[string][]string}` from `ListRules` + `ListPrincipals`.

Wire into `cmd/silo/serve.go`: mount `web.Handler` at `/` and move the git transport handler to a sub-mux that matches `*.git/` paths.

## Routes

```
GET /                          repo list: owner/name, ref count, last RSL entry time
GET /:owner/:repo              refs table (heads, tags, gittuf/*) with tip sha + link;
                               README rendered via git-pkgs/markup if present
GET /:owner/:repo/log/:ref     commit list: sha, author, date, message first line; ?after=<sha>
GET /:owner/:repo/commit/:sha  one commit: metadata, parents, changed files (names only)
GET /:owner/:repo/rsl          WalkRSL as a table: #, ref, target sha (linked to /commit/),
                               signer fingerprint, age; row tinted green if VerifyRef(ref)
                               currently passes, red if not
GET /:owner/:repo/policy       Rules table: name, patterns, threshold, principals;
                               Principals table: ID, key fingerprints
```

Every page links to `/rsl` and `/policy` in the header. Unknown repo → 404 with the same layout.

## Acceptance

`testdata/testscript/04_web.txtar`: reuse 03's setup (alice's policy + push), then `curl -fsS` each route and assert:

```
exec curl -fsS http://$SILO_HTTP/
stdout 'alice/demo'
exec curl -fsS http://$SILO_HTTP/alice/demo
stdout 'refs/gittuf/reference-state-log'
exec curl -fsS http://$SILO_HTTP/alice/demo/rsl
stdout 'refs/heads/main'
stdout 'protect-main|alice'
exec curl -fsS http://$SILO_HTTP/alice/demo/policy
stdout 'protect-main'
stdout 'git:refs/heads/main'
stdout 'alice'
exec curl -fsS http://$SILO_HTTP/alice/demo/log/main
stdout 'one'
! exec curl -fsS http://$SILO_HTTP/nobody/nothing
```

Unit: `TestWalkRSL` against a repo with two recorded entries asserts order (newest first) and field extraction. `TestHandler_Routes` with `httptest.Server` asserts each route returns 200 and `Content-Type: text/html`.

**Done when:** `04_web.txtar` passes; opening `http://localhost:8080/alice/demo/rsl` in a browser after running 03's manual steps shows the entry alice pushed, with her key fingerprint, and the row is green.
