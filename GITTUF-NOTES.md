# gittuf integration notes

Observations from embedding `github.com/gittuf/gittuf/experimental/gittuf` server-side in silo's receive path. Each entry should make sense to a gittuf maintainer who hasn't read silo's code. See `SPEC.md` § "gittuf feedback" for when to add an entry.

Format per entry:

```
## <short title>
kind: friction | missing | bug | opportunity | question
gittuf: <file:line or func>
silo:   <file:line where it bit>
<what happened, what was expected, what was done instead>
```

---

## gitinterface shells out to system git
kind: friction
gittuf: pkg/gitinterface/repository.go:58, blob.go:14, commit.go:50, log.go:22, changes.go:28
silo:   internal/gittuf (design assumption)
Every operation in `pkg/gitinterface` calls the `git` binary via `exec.Cmd`. `LoadRepository` does `exec.LookPath("git")` and fails if absent. A forge embedding gittuf cannot ship as a single static binary without also bundling git. A go-git backend for `gitinterface` (it already has `GetGoGitRepository()` at repository.go:38, but nothing uses it for the operations) would let embedders choose.

## LoadRepository changes process working directory
kind: bug
gittuf: pkg/gitinterface/repository.go:72-75
silo:   internal/gittuf (concurrent receive-pack)
`LoadRepository` does `os.Chdir(repositoryPath)` to run `git rev-parse --git-dir`, then `os.Chdir` back. `os.Chdir` is process-global; two goroutines calling `LoadRepository` for different repos race and one will resolve the wrong gitDir. The same pattern appears at utils.go:58, status.go:107, tree.go:310. A server handling concurrent pushes hits this. The `rev-parse` call could pass `-C repositoryPath` instead of chdir; the others could set `cmd.Dir`.

## IsBare returns false for bare repos ending in .git
kind: bug
gittuf: pkg/gitinterface/repository.go:48-52
silo:   internal/gittuf
`IsBare()` returns `!strings.HasSuffix(r.gitDirPath, ".git")`. For a bare repo at `/data/repos/owner/name.git`, `rev-parse --git-dir` returns that path, which ends in `.git`, so `IsBare()` returns false. The convention for bare repos is exactly to name the directory `<name>.git`. `git rev-parse --is-bare-repository` would give the right answer.

## Principal keys are embedded by value
kind: opportunity
gittuf: internal/tuf/tuf.go:84, internal/tuf/v02/tuf.go:33-38
silo:   forge key rotation
`Principal.Keys()` returns key material inline; v02 `Person.PublicKeys` is a map of keys stored in the metadata. A forge whose witness key appears in N repos' policies needs N root re-signs to rotate. A principal type whose `Keys()` resolves from a separately-signed metadata blob (referenced by URL or ref) would let a forge rotate its own key under its own signature without touching repo roots, while still letting verifiers pin the indirection if they want.
