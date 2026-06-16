// Package signer loads the forge's witness signing key. The forge key signs
// RSL entries recording ref movements; it is a witness, not an authorising
// principal, so policy on protected refs does not depend on it.
package signer

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const (
	keyFilename = "forge.key"
	keyPerm     = 0o600
)

// Signer signs arbitrary bytes with the forge witness key.
type Signer interface {
	Sign(data []byte) ([]byte, error)
	PublicKey() crypto.PublicKey
	ID() string
	// KeyBytes returns the PEM-encoded private key, for callers that need to
	// hand the key material to a library that does its own signing (gittuf's
	// CommitUsingSpecificKey). Backends without exportable key material
	// (agent, sigstore) return ErrKeyNotExportable.
	KeyBytes() ([]byte, error)
}

// ErrKeyNotExportable is returned by KeyBytes for backends that cannot export
// raw key material.
var ErrKeyNotExportable = errors.New("signer: key material not exportable for this backend")

// ErrKeyNotFound is returned when no forge key exists yet.
var ErrKeyNotFound = errors.New("signer: forge key not found; run `silo keygen`")

// Load reads the ed25519 forge key from dataDir/forge.key. The file must be
// mode 0600 or tighter.
func Load(dataDir string) (Signer, error) { //nolint:ireturn // backend selection point
	path := filepath.Join(dataDir, keyFilename)
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&^os.FileMode(keyPerm) != 0 {
		return nil, fmt.Errorf("signer: %s has permissions %v; want 0600", path, fi.Mode().Perm())
	}
	// #nosec G304 -- path is constructed from configured SILO_DATA
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := ssh.ParseRawPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("signer: parse %s: %w", path, err)
	}
	priv, ok := k.(*ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signer: %s is not an ed25519 key", path)
	}
	return &ed25519Signer{priv: *priv}, nil
}

// Generate writes a new ed25519 forge key to dataDir/forge.key, refusing to
// overwrite an existing one.
func Generate(dataDir string) error {
	path := filepath.Join(dataDir, keyFilename)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("signer: %s already exists", path)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), keyPerm)
}

type ed25519Signer struct {
	priv ed25519.PrivateKey
}

func (s *ed25519Signer) Sign(data []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, data), nil
}

func (s *ed25519Signer) PublicKey() crypto.PublicKey {
	return s.priv.Public()
}

// ID returns the SSH SHA256 fingerprint of the public key.
func (s *ed25519Signer) ID() string {
	pk, err := ssh.NewPublicKey(s.priv.Public())
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(pk)
}

func (s *ed25519Signer) KeyBytes() ([]byte, error) {
	block, err := ssh.MarshalPrivateKey(s.priv, "")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// AuthorizedKey returns the public key in SSH authorized_keys format.
func AuthorizedKey(s Signer) (string, error) {
	pk, err := ssh.NewPublicKey(s.PublicKey())
	if err != nil {
		return "", err
	}
	return string(ssh.MarshalAuthorizedKey(pk)), nil
}
