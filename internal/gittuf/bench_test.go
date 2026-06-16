package gittuf

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
)

func benchRSLRepo(b *testing.B, n int) *git.Repository {
	b.Helper()
	repo, _ := git.Init(memory.NewStorage())
	tree := writeObj(b, repo, &object.Tree{})
	sig := object.Signature{Name: "x", When: time.Unix(1_700_000_000, 0)}
	var tip plumbing.Hash
	for i := range n {
		c := &object.Commit{
			TreeHash:  tree,
			Committer: sig,
			Message:   fmt.Sprintf("RSL Reference Entry\n\nref: refs/heads/main\ntargetID: aaa%d\nnumber: %d\n", i, i+1),
		}
		if !tip.IsZero() {
			c.ParentHashes = []plumbing.Hash{tip}
		}
		tip = writeObj(b, repo, c)
	}
	_ = repo.Storer.SetReference(plumbing.NewHashReference(RSLRef, tip))
	return repo
}

func BenchmarkWalkRSL(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			repo := benchRSLRepo(b, n)
			b.ResetTimer()
			for b.Loop() {
				if _, err := WalkRSL(context.Background(), repo); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkParseRSLCommit(b *testing.B) {
	c := &object.Commit{
		Message:   "RSL Reference Entry\n\nref: refs/heads/main\ntargetID: 0123456789abcdef0123456789abcdef01234567\nnumber: 42\n",
		Committer: object.Signature{When: time.Now()},
	}
	for b.Loop() {
		_ = parseRSLCommit(c)
	}
}
