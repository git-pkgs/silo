# silo

A small git host with [gittuf](https://gittuf.dev) wired into the receive path. Branch protection lives in the repository as a signed policy ref, every accepted push is recorded in a signed reference state log, and a clone carries both with it. Run `gittuf verify-ref main` on a fresh clone and the chain checks out against the owner's key without trusting silo at all.

It's a single Go binary using go-git for the wire protocol and gittuf as a library for verification, both via small forks pinned in `go.mod`. HTTP serves anonymous reads and a web UI; SSH handles authenticated push and private fetch. There is no token that can move a ref.

This is a prototype of [GAP-2](https://github.com/gittuf/gittuf/blob/main/docs/gaps/2/README.md) Configuration A. It works, the tests pass, and it's not something you'd put on the internet yet.

## See it running

```sh
git clone https://github.com/git-pkgs/silo
cd silo
make demo
```

That builds silo and a matching gittuf, seeds an `alice/demo` repository with a signed policy and a couple of pushes, and leaves the server on http://127.0.0.1:8080. Open the **rsl** and **policy** tabs on the repo page to see the signed log and the rules it was checked against. [docs/demo.md](docs/demo.md) walks through pushing as alice, getting rejected for an unsigned tip, getting rejected as bob, and verifying a clone offline.

## Running it for real

`go install` won't work because `go.mod` has `replace` directives for the gittuf and go-git forks. Build from a checkout:

```sh
go build -o silo ./cmd/silo
```

Generate the forge witness key and start the listeners:

```sh
./silo keygen --data ./silo-data
./silo serve  --data ./silo-data --http :8080 --ssh :2222 --base-url http://localhost:8080
```

Register a user and a repo:

```sh
./silo admin user create alice --ssh-key ~/.ssh/id_ed25519.pub --data ./silo-data
./silo admin repo create alice/demo --data ./silo-data
```

A new repository refuses every push until its owner has signed root metadata. From a working clone, with `gittuf` on your PATH (build it with `go build -o gittuf github.com/gittuf/gittuf` from this checkout so it picks up the fork):

```sh
git init -b main demo && cd demo
git remote add origin ssh://git@localhost:2222/alice/demo.git
git config user.signingKey ~/.ssh/id_ed25519
git config gpg.format ssh

git commit --allow-empty -m "initial"

gittuf trust init -k ~/.ssh/id_ed25519
gittuf trust add-policy-key -k ~/.ssh/id_ed25519 --policy-key ~/.ssh/id_ed25519.pub
gittuf policy init -k ~/.ssh/id_ed25519 --policy-name targets
gittuf policy add-person -k ~/.ssh/id_ed25519 --person-ID alice --public-key ~/.ssh/id_ed25519.pub
gittuf policy add-rule -k ~/.ssh/id_ed25519 --rule-name protect-main --rule-pattern 'git:refs/heads/main' --authorize alice
gittuf policy stage --local-only
gittuf policy apply --local-only
gittuf rsl record main --local-only

git push origin 'refs/gittuf/*:refs/gittuf/*' main
```

Yes, it's a lot of `gittuf` invocations. Most of that is one-time root setup; subsequent pushes are `gittuf rsl record main` then `git push`. Wrapping the first-run sequence in a `silo repo init-trust` helper is on the list.

After a push silo writes a witness annotation to the server-side RSL, so before recording your next entry, fetch it:

```sh
git fetch origin 'refs/gittuf/*:refs/gittuf/*'
gittuf rsl record main --local-only
git push origin 'refs/gittuf/*:refs/gittuf/*' main
```

Forget the `rsl record` and the push is rejected naming the rule, the threshold, and who would have satisfied it.

## Web UI

Open the base URL in a browser. Each repo has an overview (refs and README), a log per ref, an RSL viewer with rows tinted by current verify status and signers resolved to principal names, a policy page rendered from `refs/gittuf/policy`, and commit pages that show the relevant RSL slice plus a diff. It's read-only; there is no merge button by design.

## Commands

```
silo serve              start the HTTP and SSH listeners
silo keygen             generate the forge witness key
silo pubkey             print it in authorized_keys format
silo admin user create  add a user with an SSH key
silo admin repo create  create a bare repository
```

## Configuration

| flag | env | default |
| --- | --- | --- |
| `--data` | `SILO_DATA` | `./silo-data` |
| `--http` | `SILO_HTTP` | `:8080` |
| `--ssh` | `SILO_SSH` | `:2222` |
| `--base-url` | `SILO_BASE_URL` | `http://localhost:8080` |

## More

[docs/architecture.md](docs/architecture.md) covers the receive pipeline and disk layout. [docs/trust-model.md](docs/trust-model.md) explains why the owner signs root and the forge only witnesses. [docs/demo.md](docs/demo.md) is the show-and-tell script. The fork patches are catalogued in [GITTUF-NOTES.md](GITTUF-NOTES.md) and [GOGIT-NOTES.md](GOGIT-NOTES.md), each written as a standalone upstream issue.

MIT licensed.
