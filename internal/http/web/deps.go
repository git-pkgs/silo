package web

import (
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/merkletrie"

	"github.com/git-pkgs/silo/internal/pkgs"
)

// manifestDeltas walks the change set between two trees (parent may be nil
// for root commits) and returns a FileDelta per changed manifest. The cache
// is keyed on (oldOID, newOID, path); it is safe to pass a per-handler
// shared *DeltaCache.
func manifestDeltas(parent, current *object.Tree, cache *pkgs.DeltaCache) []*pkgs.FileDelta {
	if current == nil {
		return nil
	}
	changes, err := object.DiffTree(parent, current)
	if err != nil {
		return nil
	}
	var out []*pkgs.FileDelta
	for _, ch := range changes {
		action, aerr := ch.Action()
		if aerr != nil {
			continue
		}
		path := ch.To.Name
		if path == "" {
			path = ch.From.Name
		}
		if !pkgs.IsManifest(path) {
			continue
		}
		oldOID, newOID := "", ""
		var oldBlob, newBlob []byte
		switch action {
		case merkletrie.Insert:
			newBlob, newOID = readBlobAt(current, path)
		case merkletrie.Delete:
			oldBlob, oldOID = readBlobAt(parent, path)
		case merkletrie.Modify:
			oldBlob, oldOID = readBlobAt(parent, ch.From.Name)
			newBlob, newOID = readBlobAt(current, ch.To.Name)
		}
		var delta *pkgs.FileDelta
		if cache != nil {
			delta, _ = cache.GetOrCompute(oldOID, newOID, path, func() (*pkgs.FileDelta, error) {
				return pkgs.Textconv(path, oldBlob, newBlob)
			})
		} else {
			delta, _ = pkgs.Textconv(path, oldBlob, newBlob)
		}
		if delta == nil {
			continue
		}
		delta.Path = path
		out = append(out, delta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func readBlobAt(t *object.Tree, path string) ([]byte, string) {
	if t == nil || path == "" {
		return nil, ""
	}
	f, err := t.File(path)
	if err != nil {
		return nil, ""
	}
	rd, err := f.Reader()
	if err != nil {
		return nil, ""
	}
	defer func() { _ = rd.Close() }()
	const cap = pkgs.MaxManifestBytes + 1
	b, _ := io.ReadAll(io.LimitReader(rd, cap))
	return b, f.Hash.String()
}

// manifestViewForBlob returns the parsed dependency view for path in tree, or
// nil when the path isn't a manifest, the blob is too large, or parsing fails.
func manifestViewForBlob(t *object.Tree, path string, blob []byte) *pkgs.FileView {
	if !pkgs.IsManifest(path) || t == nil {
		return nil
	}
	v, _ := pkgs.Render(path, blob)
	return v
}

// treeAnnotation is the per-row "ecosystem · N deps" tag for the tree page.
type treeAnnotation struct {
	Ecosystem string
	Total     int
}

// annotateTreeEntries returns, for each entry name that is a recognised
// manifest, the ecosystem and dependency count. Unrecognised or oversize
// blobs are absent.
func annotateTreeEntries(t *object.Tree, entries []treeEntry) map[string]treeAnnotation {
	out := map[string]treeAnnotation{}
	if t == nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if !pkgs.IsManifest(e.Name) {
			continue
		}
		blob, _ := readBlobAt(t, e.Name)
		if blob == nil {
			continue
		}
		v, _ := pkgs.Render(e.Name, blob)
		if v == nil {
			continue
		}
		out[e.Name] = treeAnnotation{Ecosystem: v.Ecosystem, Total: v.Total}
	}
	return out
}

// commitDeltaSummary aggregates +/-/~ counts across all deltas for the
// commit-page header.
func commitDeltaSummary(deltas []*pkgs.FileDelta) (added, removed, updated int) {
	for _, d := range deltas {
		added += len(d.Added)
		removed += len(d.Removed)
		updated += len(d.Updated)
	}
	return added, removed, updated
}

// Unused imports avoidance: keep go-git symbols referenced if the package is
// trimmed; harmless at runtime.
var (
	_ = git.LogOrderCommitterTime
	_ = plumbing.ZeroHash
	_ = strings.TrimSpace
	_ = http.MethodGet
)
