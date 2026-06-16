# Architecture

silo is a git server whose receive path runs gittuf verification before refs move and records a witnessed RSL entry after they do. Everything else (HTTP upload-pack, the web UI, the API) is a read-only view over bare repositories on disk.

## Receive path

```
client ──ssh──> internal/ssh ──> internal/receive.ReceivePack
                                     │
                                     └─> go-git transport.ReceivePack
                                            with Hooks{PreReceive, PostReceive}
                                                  │
                       ┌──────────────────────────┴──────────────────────────┐
                       │ PreReceive (internal/hooks.Builtin):                │
                       │   flock silo.lock; apply gittuf refs; HasSignedRoot │
                       │   gate; apply other refs; VerifyRef each; rollback  │
                       │   so go-git's updateReferences applies cleanly      │
                       │ PostReceive:                                        │
                       │   Witness annotation on the pushed RSL tip,         │
                       │   signed by forge.key; release lock                 │
                       └─────────────────────────────────────────────────────┘
```

`internal/receive` is a thin adapter (~120 lines) over go-git v6's `transport.ReceivePack`, which the `git-pkgs/go-git@silo` fork extends with `ReceivePackHooks`. A `*receive.RejectionError` from PreReceive refuses every ref in the push, writes a multi-line explanation on sideband band 2, and fills `ng <ref>` in report-status. The pack stream is wrapped in a limit reader bounded by `MaxPackBytes`.

## Disk

```
$SILO_DATA/
  silo.db                  users, ssh_keys, tokens, repos, repo_members, jobs
  forge.key                ed25519 witness key
  host_ed25519             SSH host key
  repos/
    <owner>/<name>.git/    bare go-git repository
      refs/gittuf/         reference-state-log, policy, attestations
      silo.lock            flock target for the receive critical section
      hooks.d/             user exec hooks
```

`silo.db` is sqlite via modernc.org/sqlite (pure Go). Everything else about a repository is in the repository.

## Packages

```
cmd/silo            cobra CLI
internal/config     flags + SILO_* env
internal/gitstore   open/create bare repos under $SILO_DATA/repos
internal/receive    adapter over go-git transport.ReceivePack with hook plumbing
internal/http/git   smart-HTTP upload-pack (anon read only)
internal/http/web   server-rendered HTML UI; one handler per route, basecoat CSS
internal/ssh        gliderlabs/ssh, key→user, dispatch to receive/upload-pack
internal/store      silo.db schema and queries
internal/signer     forge witness key (ed25519 file; agent and sigstore backends later)
internal/gittuf     wrapper over gittuf/experimental/gittuf: Open, VerifyRef,
                    VerifyMergeable, RuleFor, Policy, Hooks, Witness, WalkRSL
internal/hooks      builtin gittuf verify/witness + exec hooks.d/*
```

## Transports

HTTP serves `info/refs?service=git-upload-pack` and `git-upload-pack` only; receive-pack returns 404. SSH serves both upload-pack and receive-pack and is the only way to push. API tokens authenticate `/api/v1` and cannot move refs.

## Web

`cmd/silo/serve.go` dispatches at the top level: `/static/` to the embedded stylesheet handler, `*.git/{info/refs,git-upload-pack}` to the git transport, everything else to `internal/http/web`. Each web handler opens the repo via `gitstore`, builds a `page` struct (repo path, active tab, refs, verify-badge count), and renders an `html/template` from `templates/{layout,pages}` or encodes the same struct as JSON when `?format=json` is set. The verify badge runs `VerifyRef` on every non-gittuf ref per request; there is no cache yet. See [web.md](web.md) for the route list.

## Dependencies

go-git provides packfile and pkt-line handling. gittuf's `pkg/gitinterface` shells out to the system `git` binary for object and ref operations, so a `git` on PATH is required wherever silo runs verification or RSL recording.
