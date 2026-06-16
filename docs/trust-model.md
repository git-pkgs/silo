# Trust model

silo enforces gittuf policy on push and records ref movements in the repository's reference state log. Verification of that log against policy is the client's job, and works without trusting silo.

## Owner signs root

Creating a repository on silo produces a bare repo with no policy. Pushes to anything outside `refs/gittuf/*` are refused until the owner has pushed `refs/gittuf/policy` containing root metadata signed by their own key. silo does not sign the root: if it did, a verifier walking back from any commit would bottom out at metadata whose only authority is silo itself. With the owner signing, `gittuf verify-ref` on a clone terminates at a key the owner controls, and a compromised silo cannot retroactively change who that was.

The owner does this locally:

```
gittuf trust init -k <root-key>
gittuf trust add-policy-key -k <root-key> --policy-key <policy-key>.pub
gittuf policy init -k <policy-key> --policy-name targets
gittuf policy add-person -k <policy-key> --person-ID <name> --public-key <key>.pub
gittuf policy add-rule -k <policy-key> --rule-name <rule> --rule-pattern git:refs/heads/main --authorize <name>
gittuf policy stage --local-only
gittuf policy apply --local-only
git push origin 'refs/gittuf/*:refs/gittuf/*'
```

## Witness, not authoriser

silo holds a signing key (`silo keygen`, printed by `silo pubkey`). The intent is for that key to appear in repository policies as a witness: silo records that a ref moved, on behalf of an authenticated user, but the forge key alone does not satisfy policy on protected refs. A push that advances `refs/heads/main` must carry an RSL entry signed by a key the policy authorises. A plain `git push` from a client that hasn't run `gittuf rsl record` fails policy, which is the honest position. Repositories that want forge-key-is-sufficient can add it to their authorising set explicitly, and that choice is recorded in signed policy.

## What silo enforces and what it doesn't

On every push silo applies the proposed ref updates under a per-repo `flock`, then runs `gittuf verify-ref` on each non-gittuf ref. Verification walks the RSL, loads the policy in force at each entry, and checks that an authorised key signed it. On failure all updates are rolled back and the client sees which rule was unsatisfied and which principals would have satisfied it.

silo's enforcement is defence in depth. The property that matters is that a fresh clone carries `refs/gittuf/policy` and `refs/gittuf/reference-state-log` with it, and `gittuf verify-ref` on that clone checks the chain back to the owner's root key without consulting silo. A compromised silo could serve a tampered ref tip, but it cannot produce a valid RSL entry for it without a key the policy authorises, and a verifying client stops at the last entry that does.

## Crash window

The receive sequence applies refs and then records the witness annotation, both under the same lock. A crash between the two leaves the ref tip ahead of any silo-side record. The ref movement is still backed by the client's own RSL entry (which was applied first), so a verifying client sees a valid chain. silo logs the gap on next start; the next push to that ref closes it.

## Import

A repository imported with existing history gets one genesis RSL entry once the owner has signed root. Commits before that entry are unverifiable by construction; verification reports the chain as starting at genesis rather than claiming coverage it doesn't have.
