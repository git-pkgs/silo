# silo demo

A back-and-forth between a terminal and a browser. The point to land: branch protection and the audit log live in the repository as signed git refs, the forge enforces and witnesses them, and a clone can verify the chain without trusting the forge.

Everything below assumes you're in the silo checkout.

## Bring it up

```sh
make demo
```

Builds silo and a gittuf binary into `.demo/bin/`, generates `alice` and `bob` SSH keypairs, creates `alice/demo`, has alice sign a gittuf root and a `protect-main` rule, makes a couple of commits, and pushes twice. Bob is registered with silo but not named in the policy. The server is left running on `:8080`. Keep this terminal open.

In a second terminal, set up a working shell:

```sh
cd .demo/work/repo
export PATH="$PWD/../../bin:$PATH"
export GIT_CONFIG_GLOBAL=/dev/null GITTUF_DEV=1
alias as-alice='GIT_SSH_COMMAND="ssh -i $PWD/../../keys/alice -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222"'
alias as-bob='GIT_SSH_COMMAND="ssh -i $PWD/../../keys/bob -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222"'
```

**Browser:** open http://127.0.0.1:8080/. One repo, `alice/demo`.

## The policy

**Browser:** click into `alice/demo`, then the **policy** tab.

Rules: `protect-main` covers `git:refs/heads/main`, threshold 1, principal `alice`. Principals: `alice` and her SSH fingerprint.

This is rendered from `refs/gittuf/policy` inside the repo, signed by alice's key. There is no settings row. An admin on the server cannot widen this rule without alice's key.

**CLI:** show the same thing from the working clone, no server involved:

```sh
gittuf policy list-rules
```

Same rule, read from the local ref.

## The log

**Browser:** click the **rsl** tab.

Six entries. The `refs/heads/main` rows are green: silo ran `gittuf verify-ref main` and the chain holds. Signer column reads `alice` on the reference entries and `silo` on the witness annotations (hover for fingerprints). Each push produced a pair: alice's signed "main is now at X" and silo's signed "I authenticated alice for that push".

## A push that lands

**CLI:**

```sh
echo "fourth line" >> README.md
git commit -q -am "fourth"
as-alice git fetch -q origin 'refs/gittuf/*:refs/gittuf/*'
gittuf rsl record main --local-only
as-alice git push origin 'refs/gittuf/*:refs/gittuf/*' main
```

The fetch pulls silo's last witness annotation so the local RSL extends cleanly; `rsl record` signs the new tip with alice's key; the push sends both refs.

**Browser:** refresh the **rsl** tab. Two new rows at the top: entry #7 `alice` moved main, entry #8 `silo` witnessed it. Click the target sha on #7.

The commit page shows author/date/parent, then a "Reference state log" table with just those two rows (one green, one annotation), then changed files (each linked to the blob view) and a coloured diff with `+fourth line`. Click `README.md` in the file list to see the rendered markdown, then **blame** in the subhead to see which commit owns each line. This is "here's a change and here's who authorised it" without the reader needing to know what an RSL is.

While here: the **verify** tab shows `all 1 refs verify` with main green. The **branches** tab shows main as default with a `protect-main` rule link and a green verify cell. **policy → history** lists each policy change alice signed (`Add rule 'protect-main'`, `Add principal 'alice'`, …) with a collapsible JSON diff of `metadata/targets.json`. **policy → principal alice** lists her key, the rules naming her, and every RSL entry her key signed.

## A push without an RSL entry

**CLI:**

```sh
echo "fifth line" >> README.md
git commit -q -am "fifth"
as-alice git push origin main
```

No `rsl record` this time. The push is rejected:

```
remote: silo: rejected refs/heads/main
remote:   rule 'protect-main' requires 1 of: alice
remote:   you pushed as: alice (...) — in set
remote:   approvals on record: 0/1
remote:   policy: http://127.0.0.1:8080/alice/demo/policy#protect-main
```

Alice is in the authorising set but didn't sign for this tip, so 0/1.

**Browser:** refresh **rsl**. Still ends at #8. Nothing was appended; refs didn't move.

**CLI:** do it properly:

```sh
as-alice git fetch -q origin 'refs/gittuf/*:refs/gittuf/*'
gittuf rsl record main --local-only
as-alice git push origin 'refs/gittuf/*:refs/gittuf/*' main
```

**Browser:** refresh. #9 and #10.

## A push from someone the policy doesn't name

**CLI:**

```sh
echo "sixth line" >> README.md
git commit -q -am "sixth"
as-bob git push origin main
```

Bob's SSH key is registered with silo, so transport auth passes. But the policy doesn't name him:

```
remote: silo: rejected refs/heads/main
remote:   rule 'protect-main' requires 1 of: alice
remote:   you pushed as: bob (...) — not in principal set
```

Transport access and policy authorisation are different things.

**Browser:** **rsl** unchanged. The **verify** tab still shows all green, because the rejected push never moved a ref.

```sh
git reset -q --hard HEAD^
```

Drop bob's commit so the working tree matches the server again.

## A branch and a compare

**CLI:**

```sh
git switch -c topic
echo "topic line" >> README.md
git commit -q -am "topic"
as-alice git fetch -q origin 'refs/gittuf/*:refs/gittuf/*'
gittuf rsl record topic --local-only
as-alice git push origin 'refs/gittuf/*:refs/gittuf/*' topic
```

**Browser:** open **branches**. `topic` is listed `+1 -0` against main with a **compare** link. Click it.

The compare page shows the diff and a panel with the `protect-main` rule, threshold 1 of alice, gittuf's mergeable verdict, and a "from a clone where you hold an authorising key" block with the exact `git fetch` / `git merge` / `gittuf rsl record main` / `git push` sequence to land it. There is no merge button; that block is what replaces it.

## Breaking something on purpose

**CLI:** write a ref straight into the server's bare repo, bypassing receive-pack entirely:

```sh
git -C ../../data/repos/alice/demo.git update-ref refs/heads/rogue HEAD
```

**Browser:** every tab now shows a red **1** badge on **verify**. Click it: `refs/heads/rogue` is listed red with "no RSL entry for this tip" and a verify failure. This is what a compromised forge writing refs out-of-band looks like to anyone watching.

## Verifying without the forge

**CLI:**

```sh
cd /tmp && rm -rf verify && git clone -q http://127.0.0.1:8080/alice/demo.git verify && cd verify
git fetch -q origin 'refs/gittuf/*:refs/gittuf/*'
gittuf verify-ref main
```

This walks the RSL and policy from the clone alone and prints success. The forge served bytes; the verifier trusted only signatures. If silo had moved `main` without an authorising RSL entry, this is where it would show. Shut the server down and run it again to make the point: same result.

## What this is

A working prototype of GAP-2 "gittuf on the Forge", Configuration A. Single Go binary, pure go-git on the wire, gittuf via `experimental/gittuf` with fixes on `git-pkgs/gittuf@silo` and `git-pkgs/go-git@silo` that are individually upstreamable.
