# Push errors

When silo refuses a push it writes one or more lines to git's progress sideband (the client renders them prefixed with `remote:`) and a `! [remote rejected]` line per ref with a short reason. Nothing is applied; the push can be retried after fixing the cause.

## repo not initialised

```
remote: silo: rejected refs/heads/main
remote:   repo not initialised: run `gittuf trust init` and push refs/gittuf/policy
 ! [remote rejected] main -> main (repo not initialised: ...)
```

The repository has no signed root metadata at `refs/gittuf/policy`. Pushes to `refs/gittuf/*` are accepted so the owner can establish policy; pushes to anything else are refused until they have. See `docs/trust-model.md` for the owner's setup commands.

## policy

```
remote: silo: rejected refs/heads/main
remote:   rule 'protect-main' requires 2 of: alice, bob, carol
remote:   you pushed as: andrew (SHA256:tL3x...) — not in principal set
remote:   approvals on record: 0/2
remote:   policy: https://silo.example.com/andrew/demo/policy#protect-main
 ! [remote rejected] main -> main (policy)
```

The push advanced a ref governed by a policy rule, and the RSL entry for that movement was not signed by enough authorised principals. The lines name the rule, its threshold, the principals who could satisfy it, who silo authenticated the push as, how many approvals are already recorded as attestations, and a link to the rendered policy.

If you are in the principal set: run `gittuf rsl record <ref> --local-only` before pushing, and push `refs/gittuf/reference-state-log` alongside the ref. If you are not, a principal who is can either record the RSL entry themselves or add a reference authorisation with `gittuf attest authorize`.

## unpack-limit

```
 ! [remote rejected] main -> main (unpack failed)
```

with `unpack-limit` in the unpack status: the pushed packfile exceeded `MaxPackBytes` (default 512 MiB) or `MaxObjects` (default 100 000). Split the push or ask the operator to raise the limit.

## pre-receive declined

A non-gittuf pre-receive hook in `hooks.d/` exited non-zero. The hook's own output appears on the progress sideband above this line.
