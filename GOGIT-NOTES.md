# go-git integration notes

Findings from using go-git server-side in silo. silo targets `github.com/go-git/go-git/v6` at main (currently `v6.0.0-alpha.4`+); v5.19.1 is the last stable tag but main has 1300+ commits of server-side work on top of it. Each entry below is self-contained and written against v6 main as of 2026-06-16.

v6 ships a `backend` package — `Backend.Serve` / `ServeConn` / `ServeHTTP` over a `Loader` — that handles upload-pack and receive-pack across TCP, SSH, and HTTP. That's the surface silo's `internal/http/git` and `internal/ssh` should sit on once the receive-pack hook below lands; for now silo owns receive-pack and reuses v6's `packp`/`pktline`/`sideband`/`packfile` packages directly, all of which work without modification.

---

## `transport.ReceivePack` has no pre-receive hook callback

**What happens.** A server using `transport.ReceivePack` cannot run policy checks between unpack and ref update. `plumbing/transport/receive_pack.go` decodes, unpacks, then calls `updateReferences` directly. The line `// TODO: Pass the options to the server-side hooks.` sits between decode and update, so this is planned.

**Why it matters.** Any forge wanting branch protection, signed-commit checks, or gittuf verification needs to inspect proposed updates after objects are available but before refs move. Without it, every such project re-implements receive-pack on top of `packp.UpdateRequests` + `packfile.UpdateObjectStorage` + `Storer.SetReference`, which is what silo does in ~200 lines.

**Suggested change.** A `Hooks` field on `ReceivePackRequest` with `PreReceive(ctx, []*packp.Command) error` called after unpack and before `updateReferences`; non-nil error fills `cmdStatus` with `ng <ref> <err>` and skips the update. A `PostReceive` after ref apply rounds it out. Possibly tracked under #2185.

**What silo would do once this lands.** `internal/receive` shrinks to a `Hooks` adapter; `internal/http/git` and `internal/ssh` become thin wrappers over `backend.Backend`.

---

## `PlainOpenWithOptions` with `DetectDotGit: true` rejects bare repositories

**What happens.** `PlainOpenWithOptions("/path/to/bare.git", &PlainOpenOptions{DetectDotGit: true})` returns `ErrRepositoryNotExists`. The same path with `DetectDotGit: false` opens.

**Why.** `dotGitToOSFilesystems` (`repository.go:467` on main) does `fs.Stat(GitDirName)` — looking for a `.git` entry inside the given path — and on miss with detect on walks up the tree. It never checks whether the given path is itself a git directory (has `HEAD`, `objects/`, `refs/`). A bare repo has no `.git` inside it, so the walk finds nothing.

This is arguably correct behaviour (the option says "detect `.git`"), but it's a footgun: callers that already have the git dir resolved and pass `DetectDotGit: true` for safety break on bare repos. gittuf hits this; see `GITTUF-NOTES.md`.

**To reproduce.**

```go
git.PlainOpenWithOptions("/tmp/bare.git", &git.PlainOpenOptions{DetectDotGit: true})
// repository does not exist
git.PlainOpenWithOptions("/tmp/bare.git", &git.PlainOpenOptions{DetectDotGit: false})
// nil error
```

**Suggested change.** Before walking, check if the given path itself looks like a git dir (the same check `DetectDotGit: false` falls through to). #1842 fixed the linked-worktree case in this function; this is the bare-repo sibling.

---

## No `merge-tree` equivalent

**What happens.** There's no way to compute the three-way-merged tree of two commits without a worktree. gittuf's `VerifyMergeable` shells to `git merge-tree` for this; silo's planned client-signed-merge button needs it too.

**Tracked at** [#942](https://github.com/go-git/go-git/issues/942). The historical blocker was a Go diff3; that's resolved: `epiclabs-io/diff3` relicensed to MIT, and AntGroup open-sourced [HugeSCM](https://github.com/antgroup/hugescm) with a complete pure-Go three-way merge — `modules/diferenco` for text-level diff3 (Histogram/Myers/ONP/Patience algorithms, merge/diff3/zdiff3 conflict styles, charset-aware) and `pkg/zeta/odb/merge.go` for tree-level merge with rename and mode-conflict handling — explicitly offered back as a reference implementation in the issue thread.

---

## What gittuf shells to `git` for, and the go-git equivalent

gittuf's `pkg/gitinterface` shells out for the operations below. The right column is the go-git equivalent on main. Everything a server-side gittuf needs is available pure-Go and bare-safe; the only gap is `merge-tree` (above), and the worktree operations don't apply to a forge.

| `git` invocation | go-git equivalent | bare-safe |
| --- | --- | --- |
| `cat-file -t <id>` | `Storer.EncodedObject(AnyObject, h).Type()` | yes |
| `cat-file -p <id>` | `object.DecodeObject` / `Blob.Reader` | yes |
| `cat-file -e <id>` | `Storer.HasEncodedObject(h)` | yes |
| `cat-file -s <id>` | `EncodedObject.Size()` | yes |
| `hash-object -w --stdin` | `Storer.SetEncodedObject` | yes |
| `ls-tree [-r] <tree>` | `Tree.Entries` / `Tree.Files()` | yes |
| `diff-tree --name-only -r` | `object.DiffTreeWithOptions` → `Changes` | yes |
| `rev-parse <ref>` | `Repository.ResolveRevision` | yes |
| `rev-parse <c>^{tree}` | `Commit.TreeHash` | yes |
| `rev-parse <c>^@` | `Commit.ParentHashes` | yes |
| `rev-list -n 1 <tag>` | `ResolveRevision` / `TagObject.Commit()` | yes |
| `show -s --format=%B` | `Commit.Message` | yes |
| `update-ref` / `-d` | `Storer.SetReference` / `RemoveReference` | yes |
| `symbolic-ref` | `Storer.SetReference(NewSymbolicReference)` | yes |
| `config --get-regexp` / `--local` | `Repository.Config()` / `Storer.SetConfig` | yes |
| `remote add/remove/get-url` | `CreateRemote` / `DeleteRemote` / `Remote.Config().URLs` | yes |
| `merge-base <a> <b>` | `Commit.MergeBase(other)` | yes |
| `merge-base --is-ancestor` | `Commit.IsAncestor(other)` | yes |
| `merge-tree <a> <b>` | none; see #942 / HugeSCM `diferenco` | n/a |
| `status --porcelain` | `Worktree.Status()` | worktree-only |
| `restore [--staged]` | `Worktree.Checkout` / `Reset` | worktree-only |
| `for-each-ref` | `Storer.IterReferences()` | yes |
| `log` / `rev-list` (range) | `Repository.Log(LogOptions)` | yes |

---

## Fixed on main since v5.19.1

These were issues against v5 that are resolved on main; listed so they don't get re-filed.

- `packp.UploadHaves` had `Encode` but no `Decode`. v6 has `(*UploadHaves).Decode` at `plumbing/protocol/packp/uphav.go:52`.
- `ReferenceUpdateRequest.Decode` set `Packfile` even on delete-only pushes. v6's `transport.ReceivePack` has an explicit `needPackfile` check over the commands.
- `transport/server` had no sideband support in receive-pack. v6's `transport.ReceivePack` muxes report-status over `Sideband64k`/`Sideband` when negotiated.
