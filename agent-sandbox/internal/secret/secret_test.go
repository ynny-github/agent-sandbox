package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve_BarePath(t *testing.T) {
	s, err := Resolve("/etc/token")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	fs, ok := s.(FileSource)
	if !ok || fs.Path != "/etc/token" {
		t.Fatalf("got %#v, want FileSource{/etc/token}", s)
	}
}

func TestResolve_FileScheme(t *testing.T) {
	s, err := Resolve("file:///etc/token")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	fs, ok := s.(FileSource)
	if !ok || fs.Path != "/etc/token" {
		t.Fatalf("got %#v, want FileSource{/etc/token}", s)
	}
}

func TestResolve_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	s, err := Resolve("~/tok")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	fs := s.(FileSource)
	if fs.Path != filepath.Join(home, "tok") {
		t.Errorf("path = %q, want %q", fs.Path, filepath.Join(home, "tok"))
	}
}

func TestResolve_UnsupportedScheme(t *testing.T) {
	if _, err := Resolve("op://vault/item/field"); err == nil {
		t.Fatal("expected error for op:// scheme, got nil")
	}
}

func TestResolve_Empty(t *testing.T) {
	if _, err := Resolve("   "); err == nil {
		t.Fatal("expected error for empty ref, got nil")
	}
}

func TestFileSource_Load(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(p, []byte("  ghp_secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := FileSource{Path: p}.Load()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != "ghp_secret" {
		t.Errorf("value = %q, want trimmed ghp_secret", v)
	}
}

func TestFileSource_Load_Missing(t *testing.T) {
	if _, err := (FileSource{Path: filepath.Join(t.TempDir(), "nope")}).Load(); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestFileSource_Load_Empty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(p, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileSource{Path: p}).Load(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-file error, got %v", err)
	}
}
