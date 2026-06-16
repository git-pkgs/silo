# go-git integration notes

Findings from using `github.com/go-git/go-git/v5@v5.19.1` server-side in silo, plus an inventory of operations gittuf currently shells out to `git` for that go-git can now handle. Each entry is self-contained.

Existing-issue cross-reference (searched 2026-06-16): #2185 (open) tracks git hooks generally and likely subsumes the receive-pack hook item; #1842 (closed) fixed `DetectDotGit` for linked worktrees, which is adjacent to the bare-repo case but didn't cover it.

---

## `PlainOpenWithOptions` with `DetectDotGit: true` rejects bare repositories

**What happens.** `PlainOpenWithOptions("/path/to/bare.git", &PlainOpenOptions{DetectDotGit: true})` returns `ErrRepositoryNotExists`. The same path with `DetectDotGit: false` opens.

**Why.** With detect on, `dotGitToOSFilesystems` (`repository.go:341`) walks looking for a `.git` entry; it doesn't first check whether the given path is itself a git directory (has `HEAD`, `objects/`, `refs/`). A bare repo has no `.git` inside it, so the walk finds nothing.

This is arguably correct behaviour (the option says "detect `.git`"), but it's a footgun: callers that already have the git dir resolved and pass `DetectDotGit: true` for safety silently break on bare repos. gittuf hit this; see `GITTUF-NOTES.md`.

**Suggested change.** Before walking, check if the given path itself looks like a git dir (the same heuristic `DetectDotGit: false` uses). #1842 fixed the linked-worktree case in the same function; this is the bare-repo sibling.

---

## `transport/server.ReceivePack` has no hook between unpack and ref update

**What happens.** A server using `transport/server` cannot run pre-receive checks. `server.go:238-264` unpacks the packfile then immediately calls `updateReferences` with no callback, and the `// TODO: Implement 'atomic' update of references` at `:252` notes the related gap.

**Why it matters.** Any forge wanting policy enforcement (gittuf, branch protection, signed-commit checks) needs to inspect proposed updates after objects are available but before refs move. Without a hook, every such project re-implements receive-pack.

**Suggested change.** A `Hooks` field on `rpSession` (or an option to `NewServer`) with `PreReceive(ctx, []*packp.Command) error` called between `writePackfile` and `updateReferences`; non-nil error populates `ng` statuses and skips the update.

Possibly covered by open #2185 ("Git hooks").

**What silo does instead.** Owns receive-pack: reads `packp.ReferenceUpdateRequest`, calls `packfile.UpdateObjectStorage`, runs hooks, applies refs via `Storer.SetReference`, writes `packp.ReportStatus`. About 200 lines reusing the `packp`/`pktline`/`sideband` packages, which all worked without modification.

---

## `packp.UploadHaves` has Encode but no Decode

**What happens.** A smart-HTTP server needs to decode the client's `have` lines from the upload-pack POST body. `packp.UploadRequest` decodes the wants/shallows/deepen section, but `packp.UploadHaves` (`uppackreq.go:67`) only has `Encode`.

**Suggested change.** Add `(*UploadHaves).Decode(r io.Reader) error` that scans pkt-lines for `have <hash>` until `done` or flush.

**What silo does.** A 15-line `decodeHaves` in `internal/http/git/git.go`.

---

## `ReferenceUpdateRequest.Decode` sets `Packfile` even when there is none

**What happens.** After decoding a delete-only push (every command's New is zero), `req.Packfile` is still set (`updreq_decode.go:198` `setPackfile()` assigns the remaining reader unconditionally). Reading it returns immediate EOF.

**Why it matters.** Callers can't use `req.Packfile != nil` to decide whether to unpack; they have to re-scan the commands. Minor.

**Suggested change.** Set `Packfile` only if any command's `New` is non-zero.

---

## What gittuf shells to `git` for, and whether go-git v5.19 can do it

gittuf's `pkg/gitinterface` shells out for the operations below. The right-hand column is the go-git equivalent at v5.19.1. Almost everything is covered; the user's hunch that go-git has caught up since gittuf last looked is right.

| `git` invocation | go-git equivalent | bare-safe |
|---|---|---|
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
| `merge-tree <a> <b>` | **none** — no three-way merge tree builder | n/a |
| `status --porcelain` | `Worktree.Status()` | worktree-only |
| `restore [--staged]` | `Worktree.Checkout` / `Reset` | worktree-only |
| `for-each-ref` | `Storer.IterReferences()` | yes |
| `log` / `rev-list` (range) | `Repository.Log(LogOptions)` | yes |

The only operation with no go-git equivalent is `merge-tree`, used by `gitinterface.GetMergeTree` for `VerifyMergeable`. Everything else a server-side gittuf needs (`cat-file`, `ls-tree`, `diff-tree`, `rev-parse`, `update-ref`, `merge-base`) is available pure-Go and bare-safe. The worktree operations (`status`, `restore`) don't apply to a forge.
