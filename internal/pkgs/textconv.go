package pkgs

import (
	"errors"
	"sort"
	"strings"

	"github.com/git-pkgs/manifests"
)

// MaxManifestBytes caps the input passed to manifests.Parse for one-sided
// or two-sided render calls. Mirrors the cap silo plans to land upstream in
// manifests; until that lands, this is silo's local bound.
const MaxManifestBytes = 10 * 1024 * 1024

// DepUpdate describes a name whose requirement changed between two revisions.
type DepUpdate struct {
	Name string
	PURL string
	From string
	To   string
}

// FileDelta is the rich "+kamal 1.0.0 / puma 5→6" view of one changed
// manifest. Computed statelessly from the two blob payloads.
type FileDelta struct {
	Path      string
	Ecosystem string
	Kind      manifests.Kind
	Added     []manifests.Dependency
	Removed   []manifests.Dependency
	Updated   []DepUpdate
}

// DepGroup is one runtime/dev/test scope inside a FileView.
type DepGroup struct {
	Scope manifests.Scope
	Deps  []manifests.Dependency
}

// FileView is the one-sided dependency table rendered on the blob page.
type FileView struct {
	Path      string
	Ecosystem string
	Kind      manifests.Kind
	Groups    []DepGroup
	Total     int
	Direct    int
}

// IsManifest reports whether silo should render path with the rich
// manifest view rather than the raw blob view.
func IsManifest(path string) bool {
	_, _, ok := manifests.Identify(path)
	return ok
}

// Render parses one blob into a FileView. Returns (nil, nil) for blobs over
// MaxManifestBytes, files manifests doesn't recognise, or parse failures —
// the caller should then fall through to the raw source view.
func Render(path string, blob []byte) (*FileView, error) {
	if len(blob) > MaxManifestBytes {
		return nil, nil
	}
	res, err := safeParse(path, blob)
	if err != nil || res == nil {
		return nil, nil
	}

	groups := groupByScope(res.Dependencies)
	direct := 0
	for _, d := range res.Dependencies {
		if d.Direct {
			direct++
		}
	}
	return &FileView{
		Path:      path,
		Ecosystem: res.Ecosystem,
		Kind:      res.Kind,
		Groups:    groups,
		Total:     len(res.Dependencies),
		Direct:    direct,
	}, nil
}

// Textconv parses both sides and emits the +/-/Δ delta. Either oldBlob or
// newBlob may be nil for added/removed files. Returns (nil, nil) when the
// path is unrecognised, the surviving side is too large, or both sides fail
// to parse.
func Textconv(path string, oldBlob, newBlob []byte) (*FileDelta, error) {
	if len(oldBlob) > MaxManifestBytes || len(newBlob) > MaxManifestBytes {
		return nil, nil
	}
	if oldBlob == nil && newBlob == nil {
		return nil, nil
	}
	oldRes, _ := safeParse(path, oldBlob)
	newRes, _ := safeParse(path, newBlob)
	if oldRes == nil && newRes == nil {
		return nil, nil
	}
	pickRes := newRes
	if pickRes == nil {
		pickRes = oldRes
	}

	delta := &FileDelta{
		Path:      path,
		Ecosystem: pickRes.Ecosystem,
		Kind:      pickRes.Kind,
	}
	added, removed, updated := diffDeps(deps(oldRes), deps(newRes))
	delta.Added = added
	delta.Removed = removed
	delta.Updated = updated
	return delta, nil
}

func deps(r *manifests.ParseResult) []manifests.Dependency {
	if r == nil {
		return nil
	}
	return r.Dependencies
}

// diffDeps walks two dependency sets keyed on (name, scope) and emits the
// added/removed/updated sets in stable name order.
func diffDeps(before, after []manifests.Dependency) (added, removed []manifests.Dependency, updated []DepUpdate) {
	type key struct{ name string }
	beforeMap := map[key]manifests.Dependency{}
	for _, d := range before {
		beforeMap[key{d.Name}] = d
	}
	seen := map[key]bool{}
	for _, d := range after {
		k := key{d.Name}
		seen[k] = true
		old, ok := beforeMap[k]
		if !ok {
			added = append(added, d)
			continue
		}
		if old.Version != d.Version {
			updated = append(updated, DepUpdate{
				Name: d.Name,
				PURL: d.PURL,
				From: old.Version,
				To:   d.Version,
			})
		}
	}
	for k, d := range beforeMap {
		if !seen[k] {
			removed = append(removed, d)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Name < added[j].Name })
	sort.Slice(removed, func(i, j int) bool { return removed[i].Name < removed[j].Name })
	sort.Slice(updated, func(i, j int) bool { return updated[i].Name < updated[j].Name })
	return added, removed, updated
}

func groupByScope(deps []manifests.Dependency) []DepGroup {
	byScope := map[manifests.Scope][]manifests.Dependency{}
	order := []manifests.Scope{}
	for _, d := range deps {
		s := d.Scope
		if s == "" {
			s = manifests.Runtime
		}
		if _, ok := byScope[s]; !ok {
			order = append(order, s)
		}
		byScope[s] = append(byScope[s], d)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return scopeRank(order[i]) < scopeRank(order[j])
	})
	out := make([]DepGroup, 0, len(order))
	for _, s := range order {
		group := byScope[s]
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		out = append(out, DepGroup{Scope: s, Deps: group})
	}
	return out
}

var scopeOrder = []manifests.Scope{
	manifests.Runtime,
	manifests.Development,
	manifests.Test,
	manifests.Build,
	manifests.Optional,
}

func scopeRank(s manifests.Scope) int {
	for i, v := range scopeOrder {
		if v == s {
			return i
		}
	}
	return len(scopeOrder)
}

// safeParse wraps manifests.Parse so a panic from a parser becomes
// (nil, nil) rather than killing the request. We accept nil blob payloads
// silently as "no file on this side".
func safeParse(path string, blob []byte) (res *manifests.ParseResult, err error) {
	if blob == nil {
		return nil, nil
	}
	defer func() {
		if r := recover(); r != nil {
			res = nil
			err = nil
		}
	}()
	res, err = manifests.Parse(path, blob)
	if err != nil {
		var pe *manifests.ParseError
		if errors.As(err, &pe) {
			return nil, nil
		}
		// UnknownFileError or anything else: treat as non-renderable.
		return nil, nil
	}
	return res, nil
}

// PathExtBase is a tiny helper around strings.HasSuffix, kept here so the
// commit-page renderer doesn't need to grow an extra import.
func PathExtBase(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}
