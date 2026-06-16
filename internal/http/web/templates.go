package web

import (
	"embed"
	"html/template"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

var funcs = template.FuncMap{
	"join": func(a, b string) string {
		if a == "" {
			return b
		}
		return a + "/" + b
	},
	"dir": func(p string) string {
		if d := path.Dir(p); d != "." {
			return d
		}
		return ""
	},
}

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
		t, err := template.New(name).Funcs(funcs).ParseFS(templatesFS, append(layouts, p)...)
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, nil
}
