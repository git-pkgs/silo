package gittuf

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
	"golang.org/x/crypto/ssh"
)

func TestWalkRSL(t *testing.T) {
	repo, _ := git.Init(memory.NewStorage())
	tree := writeObj(t, repo, &object.Tree{})

	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c1 := writeObj(t, repo, &object.Commit{
		TreeHash:  tree,
		Committer: object.Signature{Name: "x", When: when},
		Message:   "RSL Reference Entry\n\nref: refs/heads/main\ntargetID: aaa\nnumber: 1\n",
	})
	annMsg := pem.EncodeToMemory(&pem.Block{Type: "MESSAGE", Bytes: []byte("hello")})
	c2 := writeObj(t, repo, &object.Commit{
		TreeHash:     tree,
		ParentHashes: []plumbing.Hash{c1},
		Committer:    object.Signature{Name: "x", When: when.Add(time.Minute)},
		Message:      "RSL Annotation Entry\n\nentryID: " + c1.String() + "\nskip: false\nnumber: 2\n" + string(annMsg),
	})
	if err := repo.Storer.SetReference(plumbing.NewHashReference(RSLRef, c2)); err != nil {
		t.Fatal(err)
	}

	got, err := WalkRSL(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("WalkRSL returned %d entries, want 2", len(got))
	}
	if got[0].ID != c2.String() || got[1].ID != c1.String() {
		t.Errorf("entries not newest-first: %s then %s", got[0].ID, got[1].ID)
	}
	a, r := got[0], got[1]
	if a.Kind != "annotation" || a.Number != "2" || a.AnnotatesID != c1.String() || a.Message != "hello" {
		t.Errorf("annotation parsed wrong: %+v", a)
	}
	if r.Kind != "reference" || r.Ref != "refs/heads/main" || r.TargetID != "aaa" || r.Number != "1" {
		t.Errorf("reference parsed wrong: %+v", r)
	}
	if !r.Timestamp.Equal(when) {
		t.Errorf("Timestamp = %v, want %v", r.Timestamp, when)
	}
}

func TestWalkRSL_NoRef(t *testing.T) {
	repo, _ := git.Init(memory.NewStorage())
	got, err := WalkRSL(context.Background(), repo)
	if err != nil || got != nil {
		t.Errorf("WalkRSL on empty repo = %v, %v; want nil, nil", got, err)
	}
}

func TestSignerFingerprint(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(signer.PublicKey())
	sig, err := signer.Sign(rand.Reader, []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	body := struct {
		Magic         [6]byte
		Version       uint32
		PublicKey     []byte
		Namespace     string
		Reserved      string
		HashAlgorithm string
		Signature     []byte
	}{
		Magic:         [6]byte{'S', 'S', 'H', 'S', 'I', 'G'},
		Version:       1,
		PublicKey:     signer.PublicKey().Marshal(),
		Namespace:     "git",
		HashAlgorithm: "sha256",
		Signature:     ssh.Marshal(sig),
	}
	armored := pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: ssh.Marshal(body)})

	if got := signerFingerprint(string(armored)); got != want {
		t.Errorf("signerFingerprint = %q, want %q", got, want)
	}
	if got := signerFingerprint(""); got != "" {
		t.Errorf("signerFingerprint(empty) = %q, want empty", got)
	}
	if got := signerFingerprint("not pem"); got != "" {
		t.Errorf("signerFingerprint(garbage) = %q, want empty", got)
	}
}

type encodable interface {
	Encode(plumbing.EncodedObject) error
}

func writeObj(t *testing.T, repo *git.Repository, o encodable) plumbing.Hash {
	t.Helper()
	enc := repo.Storer.NewEncodedObject()
	if err := o.Encode(enc); err != nil {
		t.Fatal(err)
	}
	h, err := repo.Storer.SetEncodedObject(enc)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
