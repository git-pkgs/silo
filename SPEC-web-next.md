# silo spec — web UI, next pass

Candidate routes beyond `SPEC-web.md`. Nothing project-management-shaped. Conventions, quality bar, and styling (basecoat + `static/style.css`, no JIT CSS) inherited from `SPEC-web.md`.

The first group is where silo is differentiated; the second is git browsing the first group links into. Pick a slice and write acceptance txtar per route as it's built.

## gittuf-forward

- `GET /:o/:r/verify` — every non-gittuf ref with its current `VerifyRef` result, plus any ref whose tip has no RSL entry covering it. The "something's wrong here" surface; the RSL page tints individual rows but doesn't summarise. Same scan the startup check already does, so it's a render not a new computation. Link it from the header with a count badge when non-zero.
- `GET /:o/:r/compare/:a...:b` — unified diff plus a `VerifyMergeable(a, b)` panel: "merging this would satisfy `protect-main` (alice ✓, need 1 more of: bob, carol)". The read-only stand-in for a merge button.
- Same page, a "merge locally" block: the `git fetch` / `git merge --no-ff` / `gittuf rsl record` / `git push 'refs/gittuf/*' main` sequence with refs and the rule's principals filled in.
- `GET /:o/:r/tags` — per-tag `VerifyRef` status and the rule it matched. Release rules (`git:refs/tags/v*`, threshold N) are the headline use.
- `GET /:o/:r/policy/history` — `git log refs/gittuf/policy` with a structural diff per commit ("added bob to protect-main", "raised threshold to 2"). Load each policy state via `experimental/gittuf` and diff `ListRules`/`ListPrincipals` outputs.
- `GET /:o/:r/rsl/:ref` — `WalkRSL` filtered to entries whose `Ref` matches.
- `GET /:o/:r/principal/:id` — keys, rules naming them, RSL entries they signed (match `SignerKeyID` against the principal's key set).
- `GET /:o/:r/attestations` — render `refs/gittuf/attestations`.
- `GET /:o/:r/hooks` — `ListHooks` output with content hashes.
- `GET /activity` — recent RSL entries across all repos, newest first.

## git browsing

- `GET /:o/:r/tree/:ref/*path`, `GET /:o/:r/blob/:ref/*path` — blob rendering through `git-pkgs/markup`. Biggest current gap.
- `GET /:o/:r/raw/:ref/*path`.
- `GET /:o/:r/blame/:ref/*path` — go-git `git.Blame`.
- `GET /:o/:r/branches` — split from the flat refs table, ahead/behind vs default branch.
- `GET /:o/:r/archive/:ref.tar.gz`.
- Commit page: link into `/tree/` at the commit; +N/-M stats on the changed-files list (already have `c.Stats()`).
- Filename search at a ref.
- `GET /:o/:r/contributors` — author counts from a log walk.

## Polish

- Clone box on overview includes `+refs/gittuf/*:refs/gittuf/*` and a `gittuf verify-ref` hint.
- Refs dropdown grouped into heads / tags / gittuf.
- README through `git-pkgs/markup` instead of `<pre>`.
- JSON variant per page (`?format=json` or `Accept: application/json`) as the seed of `/api/v1`.

## First slice

`tree`/`blob` (everything else links into it), then `verify` (cheap, and it's the page you want open when the demo deliberately breaks something), then `compare` with `VerifyMergeable`, then `tags` with verify, then `policy/history`. Each gets a `05_web_*.txtar` and handler tests alongside.
