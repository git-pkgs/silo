package pkgs

import (
	"sync"
	"sync/atomic"
)

// DeltaCache memoises Textconv results by (oldOID, newOID, path). This is
// the placeholder until `git-pkgs/oidcache` lands; it intentionally uses a
// drop-the-whole-map eviction policy because per-pair Textconv calls are
// cheap to recompute.
type DeltaCache struct {
	m     sync.Map
	count atomic.Int64
	cap   int64
}

const defaultDeltaCacheCap = 4096

// NewDeltaCache returns a DeltaCache capped at 4096 entries.
func NewDeltaCache() *DeltaCache {
	return &DeltaCache{cap: defaultDeltaCacheCap}
}

// SetCap overrides the entry cap (test hook).
func (c *DeltaCache) SetCap(n int64) {
	c.cap = n
}

func deltaKey(oldOID, newOID, path string) string {
	return oldOID + ":" + newOID + ":" + path
}

// GetOrCompute returns the cached *FileDelta for the given OID pair and path,
// computing it via `fn` on miss. `fn` is invoked at most once per key under
// normal contention.
func (c *DeltaCache) GetOrCompute(oldOID, newOID, path string, fn func() (*FileDelta, error)) (*FileDelta, error) {
	key := deltaKey(oldOID, newOID, path)
	if v, ok := c.m.Load(key); ok {
		return v.(*FileDelta), nil
	}
	v, err := fn()
	if err != nil {
		return nil, err
	}
	if _, loaded := c.m.LoadOrStore(key, v); !loaded {
		if c.count.Add(1) > c.cap {
			c.m = sync.Map{}
			c.count.Store(0)
		}
	}
	return v, nil
}
