package web

import (
	"embed"
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed templates/layout/*.html templates/pages/*.html
var templatesFS embed.FS

// loadTemplates parses each page template together with the shared layout and
// returns a map keyed by page name (e.g. "index", "rsl").
func loadTemplates() (map[string]*template.Template, error) {
	layouts, err := fs.Glob(templatesFS, "templates/layout/*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(templatesFS, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(filepath.Base(p), ".html")
		t, err := template.New(name).ParseFS(templatesFS, append(layouts, p)...)
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, nil
}
