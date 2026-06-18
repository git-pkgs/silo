#!/usr/bin/env bash
# Bring up a local silo with one user (alice), one repo (alice/demo), a signed
# gittuf policy, and a couple of pushes, then leave the server running so you
# can browse http://localhost:8080.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO="${SILO_DEMO_DIR:-$ROOT/.demo}"
HTTP="${SILO_HTTP:-127.0.0.1:8080}"
SSH_PORT="${SILO_SSH_PORT:-2222}"
SSH_ADDR="127.0.0.1:$SSH_PORT"

if [ -e "$DEMO" ] && [ "${1:-}" != "--keep" ]; then
  echo "removing $DEMO (pass --keep to reuse)"
  rm -rf "$DEMO"
fi
mkdir -p "$DEMO/bin" "$DEMO/data" "$DEMO/keys" "$DEMO/work"

echo "building silo and gittuf"
go build -o "$DEMO/bin/silo" "$ROOT/cmd/silo"
go build -o "$DEMO/bin/gittuf" github.com/gittuf/gittuf
PATH="$DEMO/bin:$PATH"

silo keygen --data "$DEMO/data"
ssh-keygen -q -t ed25519 -N "" -f "$DEMO/keys/alice" -C alice
ssh-keygen -q -t ed25519 -N "" -f "$DEMO/keys/bob" -C bob
silo admin user create alice --ssh-key "$DEMO/keys/alice.pub" --data "$DEMO/data"
silo admin user create bob --ssh-key "$DEMO/keys/bob.pub" --data "$DEMO/data"
silo admin repo create alice/demo --data "$DEMO/data"

echo "starting silo serve on http://$HTTP (ssh $SSH_ADDR)"
silo serve --data "$DEMO/data" --http "$HTTP" --ssh "$SSH_ADDR" --base-url "http://$HTTP" &
SILO_PID=$!
trap 'kill $SILO_PID 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  nc -z 127.0.0.1 "$SSH_PORT" 2>/dev/null && break
  sleep 0.1
done

export GIT_SSH_COMMAND="ssh -i $DEMO/keys/alice -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p $SSH_PORT"
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GITTUF_DEV=1

cd "$DEMO/work"
git init -q -b main repo
cd repo
git config user.name alice
git config user.email alice@example.com
git config user.signingKey "$DEMO/keys/alice"
git config gpg.format ssh
git remote add origin "ssh://git@127.0.0.1:$SSH_PORT/alice/demo.git"

cat > README.md <<'EOF'
# demo

A repository whose branch protection lives in the repo itself, as a gittuf
policy at `refs/gittuf/policy`, with every accepted ref movement recorded in a
signed log at `refs/gittuf/reference-state-log`.

Clone it, run `gittuf verify-ref main`, and the chain checks out without
trusting the server.
EOF
git add README.md
git commit -q -m "add README"

echo "second line" >> README.md
git commit -q -am "expand README"
git tag -s -m "v1.0.0" v1.0.0

cat > go.mod <<'EOF'
module example.com/demo

go 1.26

require (
	github.com/spf13/cobra v1.8.0
	github.com/stretchr/testify v1.9.0
)
EOF
git add go.mod
git commit -q -m "add go.mod with two deps"

echo "initialising gittuf policy"
gittuf trust init -k "$DEMO/keys/alice"
gittuf trust add-policy-key -k "$DEMO/keys/alice" --policy-key "$DEMO/keys/alice.pub"
gittuf policy init -k "$DEMO/keys/alice" --policy-name targets
gittuf policy add-person -k "$DEMO/keys/alice" --person-ID alice --public-key "$DEMO/keys/alice.pub"
gittuf policy add-rule -k "$DEMO/keys/alice" --rule-name protect-main --rule-pattern 'git:refs/heads/main' --authorize alice
gittuf policy add-rule -k "$DEMO/keys/alice" --rule-name protect-tags --rule-pattern 'git:refs/tags/*' --authorize alice
gittuf policy stage --local-only
gittuf policy apply --local-only
gittuf rsl record main --local-only
gittuf rsl record refs/tags/v1.0.0 --local-only

echo "pushing policy + main + tag"
git push origin 'refs/gittuf/*:refs/gittuf/*' main v1.0.0

echo "third commit, second push"
# silo wrote a witness annotation to the RSL server-side; pull it before
# extending the chain locally, otherwise the next push is non-fast-forward.
git fetch -q origin 'refs/gittuf/*:refs/gittuf/*'
echo "third line" >> README.md
git commit -q -am "more README"
gittuf rsl record main --local-only
git push origin 'refs/gittuf/*:refs/gittuf/*' main

cat <<EOF

silo is running.

  index        http://$HTTP/
  repo         http://$HTTP/alice/demo
  dependencies http://$HTTP/alice/demo/dependencies
  rsl          http://$HTTP/alice/demo/rsl
  policy       http://$HTTP/alice/demo/policy
  log          http://$HTTP/alice/demo/log/refs/heads/main

  clone:  git clone http://$HTTP/alice/demo.git

  data:   $DEMO/data
  work:   $DEMO/work/repo
  keys:   $DEMO/keys/{alice,bob}
  bin:    $DEMO/bin/{silo,gittuf}

ctrl-c to stop.
EOF

wait $SILO_PID
