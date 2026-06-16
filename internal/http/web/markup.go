package web

import (
	"html/template"

	"github.com/git-pkgs/markup"
	"github.com/microcosm-cc/bluemonday"
)

var (
	markupReg = markup.NewDefaultRegistry()
	sanitize  = bluemonday.UGCPolicy()
)

// renderMarkup renders filename's content via git-pkgs/markup if the format is
// supported, then sanitizes the result. Returns "" for unsupported formats so
// the caller can fall back to a plain <pre>.
func renderMarkup(filename string, content []byte) template.HTML {
	if !markupReg.Supported(filename) {
		return ""
	}
	res, err := markupReg.Render(filename, content)
	if err != nil {
		return ""
	}
	return template.HTML(sanitize.Sanitize(res.HTML)) // #nosec G203 -- sanitized
}
