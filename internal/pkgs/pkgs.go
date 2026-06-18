// Package pkgs is silo's per-repo dependency-index facade. It opens
// <repo>.git/pkgs.sqlite3 lazily via the git-pkgs/index package, caches
// open handles in an LRU, and provides a handler for the pkgs-reindex
// job kind drained by internal/jobs.
package pkgs

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/git-pkgs/git-pkgs/index"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/store"
)

// JobKind is the registered jobs.Worker handler key.
const JobKind = "pkgs-reindex"

// DBFile is the per-bare-repo sqlite filename.
const DBFile = "pkgs.sqlite3"

// DefaultCacheSize bounds open index.Index handles in the Store LRU.
const DefaultCacheSize = 32

// ReindexPayload is the JSON encoded in the jobs.payload column for a
// pkgs-reindex job.
type ReindexPayload struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// Store is an LRU of repoPath -> *index.Index. Concurrent Index calls for the
// same repo serialise; for different repos they overlap.
type Store struct {
	cap  int
	opts index.Options

	mu    sync.Mutex
	cache map[string]*list.Element
	order *list.List
}

type cacheEntry struct {
	path string
	idx  *index.Index
	mu   sync.Mutex // serialise Reindex against List on the same db handle
}

// Open returns a new Store with DefaultCacheSize entries.
func Open(opts index.Options) *Store {
	return &Store{
		cap:   DefaultCacheSize,
		opts:  opts,
		cache: map[string]*list.Element{},
		order: list.New(),
	}
}

// SetCap overrides the LRU capacity (test hook).
func (s *Store) SetCap(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cap = n
}

// Index returns (and caches) the *index.Index for the bare repo at repoPath.
// repoPath must end in `.git`. The db is created on first Open with the
// store's configured Options.
func (s *Store) Index(repoPath string) (*index.Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if el, ok := s.cache[repoPath]; ok {
		s.order.MoveToFront(el)
		return el.Value.(*cacheEntry).idx, nil
	}

	dbPath := filepath.Join(repoPath, DBFile)
	idx, err := index.Open(repoPath, dbPath, s.opts)
	if err != nil {
		return nil, fmt.Errorf("pkgs: open %s: %w", repoPath, err)
	}

	entry := &cacheEntry{path: repoPath, idx: idx}
	el := s.order.PushFront(entry)
	s.cache[repoPath] = el
	s.evict()
	return idx, nil
}

func (s *Store) evict() {
	for s.order.Len() > s.cap {
		back := s.order.Back()
		if back == nil {
			return
		}
		entry := back.Value.(*cacheEntry)
		_ = entry.idx.Close()
		s.order.Remove(back)
		delete(s.cache, entry.path)
	}
}

// Close releases every cached handle.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for e := s.order.Front(); e != nil; e = e.Next() {
		entry := e.Value.(*cacheEntry)
		_ = entry.idx.Close()
	}
	s.order.Init()
	s.cache = map[string]*list.Element{}
}

// Reindex resolves the bare repo for owner/repo via gitstore, opens the index
// db, and walks old..new on the branch. Evicts any cached handle for this
// repo first so go-git re-reads the on-disk object store: a long-lived
// open *git.Repository can miss loose objects that were written by another
// process between the previous reindex and this one.
func (s *Store) Reindex(ctx context.Context, gst *gitstore.Store, owner, repo, branch string, old, new plumbing.Hash) error {
	repoPath, err := gst.Path(owner, repo)
	if err != nil {
		return fmt.Errorf("pkgs: path %s/%s: %w", owner, repo, err)
	}
	s.evictPath(repoPath)
	idx, err := s.Index(repoPath)
	if err != nil {
		return err
	}
	return idx.Reindex(ctx, branch, old, new)
}

func (s *Store) evictPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.cache[path]
	if !ok {
		return
	}
	entry := el.Value.(*cacheEntry)
	_ = entry.idx.Close()
	s.order.Remove(el)
	delete(s.cache, path)
}

// ReindexHandler builds the jobs.Handler that decodes a ReindexPayload and
// invokes Store.Reindex.
func ReindexHandler(ps *Store, gst *gitstore.Store) func(ctx context.Context, j store.Job) error {
	return func(ctx context.Context, j store.Job) error {
		var p ReindexPayload
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
			return fmt.Errorf("pkgs: decode payload: %w", err)
		}
		if p.Owner == "" || p.Repo == "" || p.Branch == "" {
			return fmt.Errorf("pkgs: missing owner/repo/branch in payload")
		}
		newHash := plumbing.NewHash(p.New)
		if newHash.IsZero() {
			slog.Info("pkgs: skip reindex (zero new tip)", "owner", p.Owner, "repo", p.Repo, "branch", p.Branch)
			return nil
		}
		oldHash := plumbing.NewHash(p.Old)
		return ps.Reindex(ctx, gst, p.Owner, p.Repo, p.Branch, oldHash, newHash)
	}
}
