# Architecture

silo is a git server whose receive path runs gittuf verification before refs move and records a witnessed RSL entry after they do. Everything else (HTTP upload-pack, the web UI, the API) is a read-only view over bare repositories on disk.

## Receive path

```
client ──ssh──> internal/ssh ──> internal/receive.ReceivePack
                                     │
                  ┌──────────────────┴──────────────────┐
                  │ 1. decode commands + unpack packfile │  go-git packp/packfile
                  │ 2. PreReceive: flock, gittuf verify  │  internal/hooks → internal/gittuf
                  │ 3. apply refs (all-or-nothing)       │  go-git storer
                  │ 4. PostReceive: gittuf witness RSL   │  internal/gittuf → internal/signer
                  │ 5. report-status over sideband       │
                  └───────────────────────────────────────┘
```

`PreReceive` returning a `*receive.RejectionError` refuses every ref in the push and writes a multi-line explanation on sideband band 2 followed by `ng <ref> policy` in report-status. The pack is bounded by `MaxPackBytes` and `MaxObjects` read from the 12-byte header.

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
internal/receive    receive-pack: advertise, decode, unpack, hooks, apply, report
internal/http/git   smart-HTTP upload-pack (anon read only)
internal/ssh        gliderlabs/ssh, key→user, dispatch to receive/upload-pack
internal/store      silo.db schema and queries
internal/signer     forge witness key (ed25519 file; agent and sigstore backends later)
internal/gittuf     wrapper over gittuf/experimental/gittuf
internal/hooks      builtin gittuf verify/witness + exec hooks.d/*
```

## Transports

HTTP serves `info/refs?service=git-upload-pack` and `git-upload-pack` only; receive-pack returns 404. SSH serves both upload-pack and receive-pack and is the only way to push. API tokens authenticate `/api/v1` and cannot move refs.

## Dependencies

go-git provides packfile and pkt-line handling. gittuf's `pkg/gitinterface` shells out to the system `git` binary for object and ref operations, so a `git` on PATH is required wherever silo runs verification or RSL recording.
