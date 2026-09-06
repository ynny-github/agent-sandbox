package safe_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

func TestViolation_String_NoService(t *testing.T) {
	v := safe.Violation{Source: "cli", Setting: `"run" subcommand is not allowed`}
	if got, want := v.String(), `cli: "run" subcommand is not allowed`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestViolation_String_WithService(t *testing.T) {
	v := safe.Violation{Source: "compose", Service: "web", Setting: "privileged: true is not allowed"}
	if got, want := v.String(), "compose:web: privileged: true is not allowed"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathWithin(t *testing.T) {
	cases := []struct {
		name         string
		root, target string
		want         bool
	}{
		{"same dir", "/work", "/work", true},
		{"child", "/work", "/work/sub", true},
		{"deep child", "/work", "/work/a/b/c", true},
		{"parent", "/work", "/", false},
		{"sibling", "/work", "/work2", false},
		{"dotdot escape", "/work", "/work/../etc", false},
		{"prefix not boundary", "/work", "/workshop", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safe.PathWithin(tc.root, tc.target); got != tc.want {
				t.Errorf("PathWithin(%q,%q)=%v, want %v", tc.root, tc.target, got, tc.want)
			}
		})
	}
}

func TestRealPath_ResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := safe.RealPath(link); got != safe.RealPath(real) {
		t.Errorf("RealPath(link)=%q, want %q", got, safe.RealPath(real))
	}
}

func TestRealPath_NonexistentFallsBackToClean(t *testing.T) {
	if got, want := safe.RealPath("/work/./sub/../sub"), "/work/sub"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A bind target under a symlinked directory may not exist yet (compose does not
// create it). RealPath must resolve the existing symlinked prefix and re-append
// the missing tail, so the result stays comparable to the resolved parent.
func TestRealPath_SymlinkedParent_NonexistentChild(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got := safe.RealPath(filepath.Join(link, "child"))
	want := filepath.Join(safe.RealPath(real), "child")
	if got != want {
		t.Errorf("RealPath(link/child) = %q, want %q", got, want)
	}
}
