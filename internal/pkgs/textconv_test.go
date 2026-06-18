package pkgs_test

import (
	"strings"
	"testing"

	"github.com/git-pkgs/silo/internal/pkgs"
)

const goModBefore = `module x
go 1.26
require (
	github.com/spf13/cobra v1.8.0
	github.com/stretchr/testify v1.9.0
)
`

const goModAfter = `module x
go 1.26
require (
	github.com/spf13/cobra v1.9.0
	github.com/stretchr/testify v1.9.0
	github.com/google/uuid v1.6.0
)
`

const packageJSON = `{
  "name": "x",
  "version": "1.0.0",
  "dependencies": {"left-pad": "^1.0.0"},
  "devDependencies": {"eslint": "^8.0.0"}
}`

func TestIsManifest(t *testing.T) {
	yes := []string{"go.mod", "go.sum", "package.json", "package-lock.json", "Gemfile", "Gemfile.lock", "Cargo.toml", "pom.xml"}
	no := []string{"main.go", "README.md", "go.mod.bak", "src/foo.rb"}
	for _, p := range yes {
		if !pkgs.IsManifest(p) {
			t.Errorf("want IsManifest(%q)", p)
		}
	}
	for _, p := range no {
		if pkgs.IsManifest(p) {
			t.Errorf("want !IsManifest(%q)", p)
		}
	}
}

func TestTextconv_GoMod(t *testing.T) {
	d, err := pkgs.Textconv("go.mod", []byte(goModBefore), []byte(goModAfter))
	if err != nil || d == nil {
		t.Fatalf("Textconv: %v %v", err, d)
	}
	if d.Ecosystem != "golang" {
		t.Errorf("ecosystem = %q", d.Ecosystem)
	}
	if len(d.Added) != 1 || d.Added[0].Name != "github.com/google/uuid" {
		t.Errorf("added = %#v", d.Added)
	}
	if len(d.Removed) != 0 {
		t.Errorf("removed = %#v", d.Removed)
	}
	if len(d.Updated) != 1 || d.Updated[0].From != "v1.8.0" || d.Updated[0].To != "v1.9.0" {
		t.Errorf("updated = %#v", d.Updated)
	}
}

func TestTextconv_AddedFile(t *testing.T) {
	d, err := pkgs.Textconv("go.mod", nil, []byte(goModAfter))
	if err != nil || d == nil {
		t.Fatalf("textconv: %v %v", err, d)
	}
	if len(d.Added) != 3 || len(d.Removed) != 0 || len(d.Updated) != 0 {
		t.Errorf("+%d -%d ~%d", len(d.Added), len(d.Removed), len(d.Updated))
	}
}

func TestTextconv_RemovedFile(t *testing.T) {
	d, err := pkgs.Textconv("go.mod", []byte(goModBefore), nil)
	if err != nil || d == nil {
		t.Fatalf("textconv: %v %v", err, d)
	}
	if len(d.Removed) != 2 || len(d.Added) != 0 {
		t.Errorf("+%d -%d", len(d.Added), len(d.Removed))
	}
}

func TestTextconv_Garbage(t *testing.T) {
	d, _ := pkgs.Textconv("go.mod", []byte("not a go.mod"), []byte(goModAfter))
	// Either the parser tolerates it (Added = 3) or it errors (delta from
	// the surviving side). Both behaviours are acceptable for the rich
	// view — we just must not crash and the value must be non-nil so the
	// renderer can show the new-side dep list.
	if d == nil {
		t.Fatalf("want delta, got nil")
	}
}

func TestTextconv_BothInvalid(t *testing.T) {
	d, err := pkgs.Textconv("go.mod", []byte("not a go.mod"), []byte("also not"))
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	// Either both-invalid yields nil (graceful fallback to raw diff) or
	// produces an empty delta. Both shapes are acceptable.
	if d != nil && (len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Updated) > 0) {
		t.Errorf("expected empty delta, got %+v", d)
	}
}

func TestRender_PackageJSON(t *testing.T) {
	v, err := pkgs.Render("package.json", []byte(packageJSON))
	if err != nil || v == nil {
		t.Fatalf("render: %v %v", err, v)
	}
	if v.Ecosystem != "npm" {
		t.Errorf("ecosystem = %q", v.Ecosystem)
	}
	if len(v.Groups) < 2 {
		t.Fatalf("want at least 2 scope groups, got %d", len(v.Groups))
	}
	if v.Groups[0].Scope != "runtime" {
		t.Errorf("first scope = %q, want runtime", v.Groups[0].Scope)
	}
	if v.Total != 2 {
		t.Errorf("total = %d", v.Total)
	}
}

func TestRender_Garbage(t *testing.T) {
	v, _ := pkgs.Render("go.mod", []byte("clearly not a manifest"))
	if v == nil {
		return
	}
	// Some parsers tolerate trash and produce zero deps; that is fine
	// behaviour — we must not crash and the renderer can show an empty table.
	if v.Total != 0 {
		t.Errorf("want 0 deps from garbage, got %d", v.Total)
	}
}

func TestRender_Oversize(t *testing.T) {
	big := strings.Repeat("a", pkgs.MaxManifestBytes+1)
	v, err := pkgs.Render("go.mod", []byte(big))
	if err != nil || v != nil {
		t.Errorf("oversize: want (nil,nil), got (%v, %v)", v, err)
	}
}

func TestTextconv_Oversize(t *testing.T) {
	big := strings.Repeat("a", pkgs.MaxManifestBytes+1)
	d, err := pkgs.Textconv("go.mod", []byte(big), []byte(goModAfter))
	if err != nil || d != nil {
		t.Errorf("oversize: want (nil,nil), got (%v, %v)", d, err)
	}
}

func TestRender_HostilePom(t *testing.T) {
	pom := `<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>x</groupId><artifactId>x</artifactId><version>1</version>
  <parent>
    <relativePath>../../../../etc/hostname</relativePath>
  </parent>
  <dependencies>
    <dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13</version></dependency>
  </dependencies>
</project>`
	v, err := pkgs.Render("pom.xml", []byte(pom))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if v == nil {
		return
	}
	for _, g := range v.Groups {
		for _, d := range g.Deps {
			if strings.Contains(strings.ToLower(d.Name), "hostname") {
				t.Errorf("dep mentions hostname: %v", d)
			}
		}
	}
}

func FuzzTextconv(f *testing.F) {
	f.Add("go.mod", []byte("module x"), []byte("module y"))
	f.Add("package.json", []byte("{}"), []byte("{\"name\":\"x\"}"))
	f.Add("pom.xml", []byte("<project/>"), []byte("<project/>"))
	f.Fuzz(func(_ *testing.T, path string, a, b []byte) {
		_, _ = pkgs.Textconv(path, a, b)
		_, _ = pkgs.Render(path, b)
	})
}
