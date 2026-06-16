package main_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"golang.org/x/crypto/ssh"
)

func TestScript(t *testing.T) {
	binDir := t.TempDir()
	build(t, filepath.Join(binDir, "silo"), "./cmd/silo")
	// gittuf is built through silo's own module so the replace directive
	// (gittuf => git-pkgs/gittuf) applies; go install can't be used because
	// the fork's go.mod still declares module github.com/gittuf/gittuf.
	build(t, filepath.Join(binDir, "gittuf"), "github.com/gittuf/gittuf")

	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/testscript",
		RequireExplicitExec: true,
		Setup: func(e *testscript.Env) error {
			e.Vars = append(e.Vars, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return setupEnv(e)
		},
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"waitfor":        cmdWaitfor,
			"silo-test-seed": cmdSeed,
		},
	})
}

func build(t *testing.T, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v", pkg, err)
	}
}


func setupEnv(e *testscript.Env) error {
	e.Vars = append(e.Vars,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test",
	)

	data := filepath.Join(e.WorkDir, "data")
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	e.Vars = append(e.Vars, "SILO_DATA="+data)

	httpHost, httpPort, err := freePort()
	if err != nil {
		return err
	}
	sshHost, sshPort, err := freePort()
	if err != nil {
		return err
	}
	e.Vars = append(e.Vars,
		"SILO_HTTP="+httpHost,
		"SILO_HTTP_PORT="+httpPort,
		"SILO_SSH="+sshHost,
		"SILO_SSH_PORT="+sshPort,
	)

	for _, name := range []string{"alice", "bob", "mallory"} {
		if err := writeKeypair(e.WorkDir, name); err != nil {
			return err
		}
	}
	return nil
}

func freePort() (hostport, port string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	return addr.String(), strconv.Itoa(addr.Port), nil
}

func writeKeypair(dir, name string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".pub"),
		ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

func cmdWaitfor(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitfor <addr>")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", args[0], 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			if neg {
				ts.Fatalf("waitfor: %s became reachable", args[0])
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !neg {
		ts.Fatalf("waitfor: %s not reachable after 5s", args[0])
	}
}

func cmdSeed(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: silo-test-seed <bare-path>")
	}
	bare := args[0]
	ts.Check(os.MkdirAll(filepath.Dir(bare), 0o750))
	ts.Check(run("git", "init", "--bare", "-b", "main", bare))

	work := ts.MkAbs("seed-work")
	ts.Check(run("git", "init", "-b", "main", work))
	ts.Check(os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644))
	ts.Check(run("git", "-C", work, "add", "README.md"))
	ts.Check(run("git", "-C", work,
		"-c", "user.name=test", "-c", "user.email=test@test",
		"commit", "-m", "seed"))
	ts.Check(run("git", "-C", work, "push", bare, "main"))
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
