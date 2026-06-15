# silo

A git forge with [gittuf](https://gittuf.dev) built into the receive path. Every push is verified against the repository's signed policy and recorded in its reference state log, so a clone carries the policy and the log with it and `gittuf verify-ref` runs without trusting the server.

silo is a single Go binary. HTTP serves anonymous reads; SSH serves authenticated push and private fetch. There are no push-capable bearer tokens.

## Install

```
go install github.com/git-pkgs/silo/cmd/silo@latest
```

or build from source:

```
git clone https://github.com/git-pkgs/silo
cd silo && go build -o silo ./cmd/silo
```

## Quick start

```
silo keygen --data ./silo-data
silo serve  --data ./silo-data --http :8080 --ssh :2222 &

silo admin user create alice --ssh-key ~/.ssh/id_ed25519.pub --data ./silo-data
silo admin repo create alice/demo --data ./silo-data
```

A new repo refuses pushes until the owner has signed its root policy. From a working clone:

```
git remote add origin ssh://git@localhost:2222/alice/demo.git
gittuf trust init --key ~/.ssh/id_ed25519
gittuf policy init --key ~/.ssh/id_ed25519
git push origin 'refs/gittuf/*:refs/gittuf/*'
git push origin main
```

Then from anywhere:

```
git clone http://localhost:8080/alice/demo.git
cd demo && gittuf verify-ref main
```

The verify step checks the chain back to the owner's key, not silo's.

## Commands

```
silo serve              start the HTTP and SSH listeners
silo keygen             generate the forge witness key
silo pubkey             print the forge key for inclusion in policies
silo admin user create  add a user with an SSH key
silo admin repo create  create a bare repo with a policy skeleton
```

Run `silo <command> --help` for flags.

## Configuration

Flags override environment variables which override defaults.

| flag | env | default |
| --- | --- | --- |
| `--data` | `SILO_DATA` | `./silo-data` |
| `--http` | `SILO_HTTP` | `:8080` |
| `--ssh` | `SILO_SSH` | `:2222` |
| `--base-url` | `SILO_BASE_URL` | `http://localhost:8080` |

See `docs/` for architecture, the trust model, and deployment notes.

## Licence

MIT
