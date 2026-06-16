package web

import (
	"reflect"
	"strings"
	"testing"
	"time"

	gt "github.com/git-pkgs/silo/internal/gittuf"
)

func TestCrumbs(t *testing.T) {
	got := crumbs("a/b/c")
	want := []crumb{{"a", "a"}, {"b", "a/b"}, {"c", "a/b/c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("crumbs = %+v", got)
	}
	if crumbs("") != nil {
		t.Error("crumbs(empty) should be nil")
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{0: "0 B", 500: "500 B", 2048: "2 KiB", 3 << 20: "3 MiB"}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCountLines(t *testing.T) {
	if countLines(nil) != 0 || countLines([]byte("a")) != 1 || countLines([]byte("a\nb\n")) != 3 {
		t.Error("countLines wrong")
	}
}

func TestShortRef(t *testing.T) {
	cases := map[string]string{
		"refs/heads/main": "main", "refs/tags/v1": "v1", "HEAD": "HEAD", "abc123": "abc123",
	}
	for in, want := range cases {
		if got := shortRef(in); got != want {
			t.Errorf("shortRef(%q) = %q", in, got)
		}
	}
}

func TestMergeSteps(t *testing.T) {
	s := mergeSteps("http://x", "a/b", "refs/heads/main", "feature")
	for _, want := range []string{"git fetch http://x/a/b.git feature", "git switch main", "gittuf rsl record main", "git push origin"} {
		if !strings.Contains(s, want) {
			t.Errorf("mergeSteps missing %q:\n%s", want, s)
		}
	}
}

func TestGroupRefs(t *testing.T) {
	g := groupRefs([]refRow{
		{Name: "refs/heads/main"}, {Name: "refs/tags/v1"},
		{Name: "refs/gittuf/policy"}, {Name: "refs/notes/x"},
	})
	if len(g.Heads) != 1 || len(g.Tags) != 1 || len(g.Gittuf) != 1 {
		t.Errorf("groupRefs = %+v", g)
	}
}

func TestSignerNames(t *testing.T) {
	h := &handler{forgeKeyID: "SHA256:forge"}
	ps := &gt.PolicySummary{Principals: map[string][]string{
		"alice": {"SHA256:a1", "SHA256:a2"},
		"bob":   {"SHA256:b1"},
	}}
	m := h.signerNames(ps)
	if m["SHA256:forge"] != "silo" || m["SHA256:a1"] != "alice" || m["SHA256:b1"] != "bob" {
		t.Errorf("signerNames = %v", m)
	}
	if (&handler{}).signerNames(nil)["x"] != "" {
		t.Error("nil policy should yield empty map")
	}
}

func TestAgo(t *testing.T) {
	if !strings.HasSuffix(ago(time.Now().Add(-5*time.Minute)), "m ago") {
		t.Error("minutes")
	}
	if got := ago(time.Now().Add(-48 * time.Hour)); !strings.Contains(got, "-") {
		t.Errorf("days should be a date: %q", got)
	}
}

func TestRenderDiff(t *testing.T) {
	out := string(renderDiff("diff --git a b\n--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n ctx\n"))
	for _, want := range []string{`class="h">diff`, `class="h">@@`, `class="d">-old`, `class="a">+new`, " ctx\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderDiff missing %q:\n%s", want, out)
		}
	}
	big := strings.Repeat("x\n", diffMaxBytes)
	if !strings.Contains(string(renderDiff(big)), "truncated") {
		t.Error("renderDiff should truncate")
	}
}

func TestTemplateFuncs(t *testing.T) {
	if funcs["join"].(func(string, string) string)("", "x") != "x" {
		t.Error("join empty")
	}
	if funcs["join"].(func(string, string) string)("a", "b") != "a/b" {
		t.Error("join")
	}
	if funcs["dir"].(func(string) string)("a/b") != "a" {
		t.Error("dir")
	}
	if funcs["dir"].(func(string) string)("a") != "" {
		t.Error("dir root")
	}
}
