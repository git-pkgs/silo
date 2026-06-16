package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gssh "github.com/gliderlabs/ssh"
	xssh "golang.org/x/crypto/ssh"
)

const hostKeyPerm = 0o600

// loadOrCreateHostKey returns the SSH host key from dataDir, generating a new
// ed25519 key on first run. It refuses a key file with permissions wider than
// 0600.
func loadOrCreateHostKey(dataDir string) (gssh.Signer, error) {
	path := filepath.Join(dataDir, "host_ed25519")
	// #nosec G304 -- path is constructed from configured SILO_DATA, not user input
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return generateHostKey(path)
	}
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&^os.FileMode(hostKeyPerm) != 0 {
		return nil, fmt.Errorf("host key %s has permissions %v; want 0600", path, fi.Mode().Perm())
	}
	return xssh.ParsePrivateKey(b)
}

func generateHostKey(path string) (gssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), hostKeyPerm); err != nil {
		return nil, err
	}
	return xssh.NewSignerFromKey(priv)
}
