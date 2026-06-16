package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFiles embed.FS

// StaticHandler serves the embedded stylesheet. Mount it at /static/ in the
// top-level router; it's separate from Handler because the /{owner}/{repo}
// wildcard routes conflict with /static/ under ServeMux's specificity rules.
func StaticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	return http.FileServer(http.FS(sub))
}
