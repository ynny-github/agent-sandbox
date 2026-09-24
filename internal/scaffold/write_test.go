package scaffold

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func fetchedBodies() []Fetched {
	var out []Fetched
	for _, a := range Assets {
		out = append(out, Fetched{Asset: a, Body: []byte("body of " + a.Source)})
	}
	return out
}

func TestWriteCreatesEveryAssetIncludingNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	written, skipped, err := Write(dir, fetchedBodies())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped %v in an empty directory", skipped)
	}
	if len(written) != len(Assets) {
		t.Fatalf("wrote %d files, want %d", len(written), len(Assets))
	}
	for _, a := range Assets {
		got, err := os.ReadFile(filepath.Join(dir, a.Dest))
		if err != nil {
			t.Errorf("%s: %v", a.Dest, err)
			continue
		}
		if string(got) != "body of "+a.Source {
			t.Errorf("%s: body is %q", a.Dest, got)
		}
	}
}

func TestWriteNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(existing, []byte("grown by the project"), 0o644); err != nil {
		t.Fatal(err)
	}
	written, skipped, err := Write(dir, fetchedBodies())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !slices.Contains(skipped, "command-profile.json") {
		t.Errorf("skipped = %v, want it to contain command-profile.json", skipped)
	}
	if slices.Contains(written, "command-profile.json") {
		t.Errorf("written = %v, must not contain command-profile.json", written)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "grown by the project" {
		t.Errorf("existing file was overwritten: %q", got)
	}
}

// TestWriteDoesNotWriteThroughADanglingSymlink guards the feature's headline
// guarantee. os.Stat follows symlinks, so a dangling symlink at a
// destination used to read as fs.ErrNotExist and a subsequent os.WriteFile
// would write through it -- potentially outside the project directory
// entirely, since a symlink's target need not live under dir at all. Write
// must instead treat the symlink itself as "already exists" and skip it,
// leaving both the symlink and whatever it points at untouched.
func TestWriteDoesNotWriteThroughADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escaped")

	dest := filepath.Join(dir, "command-profile.json")
	if err := os.Symlink(target, dest); err != nil {
		t.Fatal(err)
	}

	written, skipped, err := Write(dir, fetchedBodies())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !slices.Contains(skipped, "command-profile.json") {
		t.Errorf("skipped = %v, want it to contain command-profile.json", skipped)
	}
	if slices.Contains(written, "command-profile.json") {
		t.Errorf("written = %v, must not contain command-profile.json", written)
	}
	if _, err := os.Lstat(dest); err != nil {
		t.Fatalf("the dangling symlink itself was removed or replaced: %v", err)
	}
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the destination is no longer a symlink -- Write wrote through it")
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("Write created %s outside the project directory by following the dangling symlink", target)
	}
}

func TestWriteSkipsAnExistingSkillNestedDeep(t *testing.T) {
	dir := t.TempDir()
	dest := ".claude/skills/growing-a-nono-profile/SKILL.md"
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(dest)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dest), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, skipped, err := Write(dir, fetchedBodies())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !slices.Contains(skipped, dest) {
		t.Errorf("skipped = %v, want it to contain %s", skipped, dest)
	}
}
