package envflag

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_ParsesAndApplies(t *testing.T) {
	p := writeFile(t, ".env", "# a comment\n\nexport FOO=bar\nBAZ=\"quoted val\"\nQUX='single'\n")
	keys, err := Load([]string{"file:" + p})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if os.Getenv("FOO") != "bar" {
		t.Errorf("FOO = %q, want bar", os.Getenv("FOO"))
	}
	if os.Getenv("BAZ") != "quoted val" {
		t.Errorf("BAZ = %q, want 'quoted val'", os.Getenv("BAZ"))
	}
	if os.Getenv("QUX") != "single" {
		t.Errorf("QUX = %q, want single", os.Getenv("QUX"))
	}
	want := []string{"FOO", "BAZ", "QUX"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestLoad_OverridesHostEnv(t *testing.T) {
	t.Setenv("OVR_KEY", "from-host")
	p := writeFile(t, ".env", "OVR_KEY=from-file\n")
	if _, err := Load([]string{"file:" + p}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OVR_KEY") != "from-file" {
		t.Errorf("OVR_KEY = %q, want from-file (--env overrides host)", os.Getenv("OVR_KEY"))
	}
}

func TestLoad_LaterRefWins(t *testing.T) {
	a := writeFile(t, "a.env", "DUP=first\n")
	b := writeFile(t, "b.env", "DUP=second\n")
	keys, err := Load([]string{"file:" + a, "file://" + b})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DUP") != "second" {
		t.Errorf("DUP = %q, want second (later ref wins)", os.Getenv("DUP"))
	}
	if !reflect.DeepEqual(keys, []string{"DUP"}) {
		t.Errorf("keys = %v, want [DUP] deduped", keys)
	}
}

func TestLoad_BarePathAndSchemes(t *testing.T) {
	p := writeFile(t, ".env", "BARE=ok\n")
	if _, err := Load([]string{p}); err != nil { // bare path
		t.Fatalf("bare path: %v", err)
	}
	if os.Getenv("BARE") != "ok" {
		t.Errorf("bare path not loaded")
	}
}

func TestLoad_EmptyRefsNoop(t *testing.T) {
	keys, err := Load(nil)
	if err != nil || keys != nil {
		t.Errorf("Load(nil) = %v, %v; want nil, nil", keys, err)
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	if _, err := Load([]string{"file:/no/such/file.env"}); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_MalformedLineErrors(t *testing.T) {
	p := writeFile(t, ".env", "NOEQUALS\n")
	if _, err := Load([]string{"file:" + p}); err == nil {
		t.Fatal("expected error for line without '=', got nil")
	}
}
