package pkgs_test

import (
	"sync/atomic"
	"testing"

	"github.com/git-pkgs/silo/internal/pkgs"
)

func TestDeltaCache_HitsAndDropAtCap(t *testing.T) {
	c := pkgs.NewDeltaCache()
	c.SetCap(3)

	var calls atomic.Int64
	make := func() (*pkgs.FileDelta, error) {
		calls.Add(1)
		return &pkgs.FileDelta{Path: "go.mod"}, nil
	}

	// First call computes; second returns cached.
	if _, err := c.GetOrCompute("a", "b", "go.mod", make); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetOrCompute("a", "b", "go.mod", make); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("want 1 call, got %d", calls.Load())
	}

	// Exceed cap → cache wiped → next lookup is a miss.
	for i := 0; i < 4; i++ {
		_, _ = c.GetOrCompute("k", string(rune('A'+i)), "go.mod", make)
	}
	if _, err := c.GetOrCompute("a", "b", "go.mod", make); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 5 {
		t.Fatalf("expected drop-the-map eviction, calls=%d", calls.Load())
	}
}
