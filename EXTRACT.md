# Extraction candidates

Packages under `internal/` that have no silo-specific imports and a clean boundary, noted for later extraction into `github.com/git-pkgs/*`. Do not extract during the first pass; record and move on. See `SPEC.md` § Conventions.

Format per entry:

```
## git-pkgs/<name>
from:    internal/<pkg>[, internal/<pkg>...]
surface: <one line: the types/funcs that would be exported>
users:   <silo, proxy, git-pkgs CLI, any go-git host, ...>
notes:   <coupling to break before extraction, if any>
```

Expected from the design doc (confirm or revise as the code lands):

## git-pkgs/gitserve
from:    internal/receive, internal/http/git, internal/ssh
surface: ReceivePack/UploadPack over io.Reader/Writer with Hooks; http.Handler and ssh.Handler over a Loader; refStateHash, bundle-uri, upload-pack response cache, dumb-protocol static handler
users:   silo, any go-git host
notes:

## git-pkgs/oidcache
from:    internal/cache
surface: Get(kind string, oids ...Hash) ([]byte, bool); Put(kind, data, oids...)
users:   silo, proxy version-diff UI, any tool rendering from immutable git objects
notes:

## git-pkgs/codeowners
from:    internal/gittuf (the import helper)
surface: Parse(r io.Reader) []Rule{Pattern, Owners}; ToGittufRules(principals)
users:   silo repo import, any gittuf onboarding tool
notes:

---
