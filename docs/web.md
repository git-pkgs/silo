# Web routes

All paths are `GET`. Append `?format=json` (or send `Accept: application/json`) to any HTML route to receive the same data as JSON. `<ref>` accepts a full ref name, a short branch or tag name, `HEAD`, or a commit sha; for tree/blob/raw/blame the ref and the path share the URL tail and the longest prefix that resolves to a revision is taken as the ref.

```
/                                     repository list
/activity                             recent RSL entries across all repos
/static/style.css                     embedded stylesheet

/<o>/<r>                              overview: clone box, refs table, README
/<o>/<r>/tree/<ref>/<path>            tree listing with breadcrumbs
/<o>/<r>/blob/<ref>/<path>            file view; markup-rendered or <pre>
/<o>/<r>/raw/<ref>/<path>             raw bytes with detected Content-Type
/<o>/<r>/blame/<ref>/<path>           per-line attribution
/<o>/<r>/history/<ref>/<path>         commits that touched <path>
/<o>/<r>/archive/<ref>.tar.gz         tarball of the tree at <ref>
/<o>/<r>/search/<ref>?q=<substr>      filename search

/<o>/<r>/log/<ref>[?after=<sha>]      commit list, 50 per page
/<o>/<r>/commit/<sha>                 metadata, RSL slice, changed files, diff
/<o>/<r>/branches                     ahead/behind vs default, rule, verify, compare link
/<o>/<r>/tags                         per-tag verify status and rule
/<o>/<r>/compare/<base>...<head>      diff, VerifyMergeable verdict, local merge steps
/<o>/<r>/contributors                 author counts on the default branch

/<o>/<r>/rsl                          full reference state log
/<o>/<r>/rsl/<ref>                    RSL filtered to one ref + its annotations
/<o>/<r>/policy                       rules and principals from refs/gittuf/policy
/<o>/<r>/policy/history               each policy commit with signer and metadata diff
/<o>/<r>/principal/<id>               keys, rules, and RSL entries for one principal
/<o>/<r>/verify                       every non-gittuf ref's VerifyRef result + RSL coverage
/<o>/<r>/attestations                 contents of refs/gittuf/attestations
/<o>/<r>/hooks                        in-policy Lua hooks via ListHooks

/<o>/<r>/dependencies                 dependency snapshot at the default branch
/<o>/<r>/dependencies/blame           commit that introduced each current requirement
/<o>/<r>/dependencies/stats           aggregated counts by ecosystem and change type
/<o>/<r>/dependencies/<purl>          per-package history (PURL is URL-escaped)
```

Commit, compare, blob, and tree pages render manifest changes as a +/-/~ table via `templates/layout/depviews.html` instead of the raw text diff for files that parse as a known manifest. The raw diff stays available behind a `<details>`. Blob pages on manifests open on the Dependencies tab by default; pass `?view=source` to force the source view.

The JSON surface for the same data lives under `/api/v1/repos/{o}/{r}/pkgs/*`. See [api.md](api.md).

Signer fingerprints in RSL tables resolve to the principal name from policy when the key matches, or `silo` when it's the forge witness key; an unrecognised fingerprint is shown raw and tinted so it stands out.

Styling is two cached stylesheets: `basecoat.cdn.min.css` from jsdelivr for components and theme variables, and `/static/style.css` for layout. Two deferred scripts (basecoat's `all.min.js` for the dropdown, lucide for icons). No JS-generated CSS, so navigating between pages doesn't repaint.
