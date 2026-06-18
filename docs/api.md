# API

silo serves a small JSON API under `/api/v1/`. Today this is the per-repo
dependency index. The shapes are silo-specific (not Gitea-shaped) and match
`git pkgs <cmd> --format=json` field-for-field, so the CLI's output types are
reused as the response payloads.

Every dependency endpoint resolves the repo by URL path. Unknown owner or
repo returns 404. While a `pkgs-reindex` job is pending or running for the
repo, responses carry the header `X-Pkgs-Indexing: true`. Index errors
(corrupt or absent db) surface as 503 with a JSON `{"error": ...}` body.
silo never returns 5xx HTML for these endpoints.

All endpoints accept `ref` for branch selection. `ref` may be a branch name
or a commit SHA; it defaults to `main`. The actual snapshot used is the
most recent dependency snapshot at or before that commit on that branch.

## GET /api/v1/repos/{owner}/{repo}/pkgs/list

The dependency snapshot at `ref`. Filter with `ecosystem=<name>` and
`direct=true`.

```sh
curl http://silo/api/v1/repos/alice/demo/pkgs/list?ref=main
```

```json
[
  {
    "name": "github.com/spf13/cobra",
    "ecosystem": "golang",
    "purl": "pkg:golang/github.com/spf13/cobra",
    "requirement": "v1.8.0",
    "dependency_type": "runtime",
    "manifest_path": "go.mod",
    "manifest_kind": "manifest"
  }
]
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/blame

For each dependency in the current snapshot on `ref`, the commit that
introduced its current requirement. Filter with `ecosystem=<name>`.

```sh
curl http://silo/api/v1/repos/alice/demo/pkgs/blame?ref=main
```

```json
[
  {
    "name": "github.com/spf13/cobra",
    "ecosystem": "golang",
    "requirement": "v1.8.0",
    "manifest_path": "go.mod",
    "sha": "bb536129a601...",
    "author_name": "alice",
    "author_email": "alice@example.com",
    "committed_at": "2026-06-16T22:56:43Z"
  }
]
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/history/{name}

Change history for one package on `ref`. `name` is URL-escaped (slashes
must be `%2F`). Filter with `ecosystem=<name>`.

```sh
curl http://silo/api/v1/repos/alice/demo/pkgs/history/github.com%2Fspf13%2Fcobra?ref=main
```

```json
[
  {
    "sha": "bb536129a601...",
    "message": "add deps",
    "author_name": "alice",
    "name": "github.com/spf13/cobra",
    "ecosystem": "golang",
    "change_type": "added",
    "requirement": "v1.8.0",
    "manifest_path": "go.mod",
    "manifest_kind": "manifest",
    "committed_at": "2026-06-16T22:56:43Z"
  }
]
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/diff

Difference between two refs. Required: `from`, `to`. The shape matches
`git pkgs diff --format=json`.

```sh
curl 'http://silo/api/v1/repos/alice/demo/pkgs/diff?from=main^&to=main'
```

```json
{
  "modified": [
    {
      "name": "github.com/spf13/cobra",
      "ecosystem": "golang",
      "manifest_path": "go.mod",
      "from_requirement": "v1.8.0",
      "to_requirement": "v1.9.0"
    }
  ]
}
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/show/{sha}

Dependency changes introduced by the commit.

```sh
curl http://silo/api/v1/repos/alice/demo/pkgs/show/bb536129a601
```

```json
[
  {
    "name": "github.com/spf13/cobra",
    "ecosystem": "golang",
    "purl": "pkg:golang/github.com/spf13/cobra",
    "change_type": "added",
    "requirement": "v1.8.0",
    "dependency_type": "runtime",
    "manifest_path": "go.mod"
  }
]
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/stats

Aggregated counts for `ref`. The shape matches `git pkgs stats --format=json`.

```sh
curl http://silo/api/v1/repos/alice/demo/pkgs/stats?ref=main
```

```json
{
  "branch": "main",
  "commits_analyzed": 12,
  "commits_with_changes": 4,
  "current_deps": 23,
  "deps_by_ecosystem": {"golang": 18, "npm": 5},
  "total_changes": 31,
  "changes_by_type": {"added": 25, "modified": 4, "removed": 2}
}
```

## GET /api/v1/repos/{owner}/{repo}/pkgs/sbom

Build an SBOM from the snapshot at `ref` and serialise. `format` selects
the serialisation: `cyclonedx` (default, JSON), `cyclonedx-xml`, or `spdx`.

```sh
curl 'http://silo/api/v1/repos/alice/demo/pkgs/sbom?ref=main&format=cyclonedx'
```

Returns `application/vnd.cyclonedx+json` (or the SPDX/XML equivalents). The
SBOM type is unset by default; supply your own metadata downstream if you
need provenance fields beyond name/version/purl.

## Notes

- Vulnerability, outdated, and license columns are part of the proxy spec
  and not exposed here yet.
- These endpoints do not require authentication; they expose the same data
  the read-only web UI does.
- silo also serves `/api/v1/repos/{owner}/{repo}/pkgs/list` with no
  `ref`. That returns the dependency snapshot for the most recent commit
  the worker has indexed on `main`.
