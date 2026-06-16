package signer

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndLoad(t *testing.T) {
	dir := t.TempDir()

	if _, err := Load(dir); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("Load before Generate: %v, want ErrKeyNotFound", err)
	}

	if err := Generate(dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := Generate(dir); err == nil {
		t.Error("second Generate should refuse to overwrite")
	}
	if s, _ := Load(dir); s != nil {
		if pem, err := s.KeyBytes(); err != nil || !strings.Contains(string(pem), "PRIVATE KEY") {
			t.Errorf("KeyBytes = %q, %v", pem, err)
		}
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(s.ID(), "SHA256:") {
		t.Errorf("ID = %q, want SHA256: prefix", s.ID())
	}

	sig, err := s.Sign([]byte("hello"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pub := s.PublicKey().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte("hello"), sig) {
		t.Error("signature does not verify")
	}

	ak, err := AuthorizedKey(s)
	if err != nil {
		t.Fatalf("AuthorizedKey: %v", err)
	}
	if !strings.HasPrefix(ak, "ssh-ed25519 ") {
		t.Errorf("AuthorizedKey = %q", ak)
	}
}

func TestLoad_BadPerms(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "forge.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load with 0644 perms should fail")
	}
}

func TestLoad_NotEd25519(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load with garbage should fail")
	}
}
