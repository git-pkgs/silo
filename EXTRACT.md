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
notes:   go-git v6 (alpha.4) has a `backend` package with the same shape (Backend.Serve/ServeConn/ServeHTTP over a Loader) and a top-level `transport.ReceivePack`. The missing piece there is the pre-receive hook callback (a TODO in v6's receive_pack.go). Likely better contributed upstream than maintained as a separate module: the hook callback, the upload-pack response cache, and bundle-uri are the deltas. See GOGIT-NOTES.md § "go-git v6 status".

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
