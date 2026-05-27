package nono

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// --- scanDir ---

func TestScanDir_Basic(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "file.txt"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".hidden"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, ".hiddendir"), 0755); err != nil {
		t.Fatal(err)
	}

	dirs, files, err := scanDir(tmp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !elementsMatch(dirs, []string{"./subdir", "./.hiddendir"}) {
		t.Errorf("dirs = %v, want [./subdir ./.hiddendir]", dirs)
	}
	if !elementsMatch(files, []string{"./file.txt", "./.hidden"}) {
		t.Errorf("files = %v, want [./file.txt ./.hidden]", files)
	}
}

func TestScanDir_DenyWrite(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.sum"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	dirs, files, err := scanDir(tmp, []string{".git", "go.sum"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != "./subdir" {
		t.Errorf("dirs = %v, want [./subdir]", dirs)
	}
	if len(files) != 1 || files[0] != "./go.mod" {
		t.Errorf("files = %v, want [./go.mod]", files)
	}
}

func TestScanDir_NonexistentDir(t *testing.T) {
	dirs, files, err := scanDir("/nonexistent/path/that/does/not/exist", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if dirs != nil || files != nil {
		t.Errorf("expected nil dirs/files on error")
	}
}

func TestScanDir_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	dirs, files, err := scanDir(tmp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dirs != nil {
		t.Errorf("dirs = %v, want nil", dirs)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

// --- deepMerge ---

func TestDeepMerge_ScalarChildWins(t *testing.T) {
	base := map[string]any{"key": "base-value", "other": "base-other"}
	child := map[string]any{"key": "child-value"}
	result := deepMerge(base, child)
	if result["key"] != "child-value" {
		t.Errorf("key = %v, want child-value", result["key"])
	}
	if result["other"] != "base-other" {
		t.Errorf("other = %v, want base-other", result["other"])
	}
}

func TestDeepMerge_ArraysDeduplicatedBaseFirst(t *testing.T) {
	base := map[string]any{"arr": []any{"a", "b"}}
	child := map[string]any{"arr": []any{"b", "c"}}
	result := deepMerge(base, child)
	arr, ok := result["arr"].([]any)
	if !ok {
		t.Fatal("arr not a []any")
	}
	want := []any{"a", "b", "c"}
	if !slicesEqual(arr, want) {
		t.Errorf("arr = %v, want %v", arr, want)
	}
}

func TestDeepMerge_MapsRecursive(t *testing.T) {
	base := map[string]any{
		"fs": map[string]any{
			"allow": []any{"~/.claude"},
			"deny":  []any{"/tmp"},
		},
	}
	child := map[string]any{
		"fs": map[string]any{
			"allow": []any{"~/.config"},
		},
	}
	result := deepMerge(base, child)
	fs, ok := result["fs"].(map[string]any)
	if !ok {
		t.Fatal("fs not a map")
	}
	allow, ok := fs["allow"].([]any)
	if !ok {
		t.Fatal("allow not a []any")
	}
	if !slicesEqual(allow, []any{"~/.claude", "~/.config"}) {
		t.Errorf("allow = %v, want [~/.claude ~/.config]", allow)
	}
	deny, ok := fs["deny"].([]any)
	if !ok || !slicesEqual(deny, []any{"/tmp"}) {
		t.Errorf("deny = %v, want [/tmp]", deny)
	}
}

func TestDeepMerge_BaseOnlyKey(t *testing.T) {
	result := deepMerge(map[string]any{"base-only": "val"}, map[string]any{})
	if result["base-only"] != "val" {
		t.Errorf("base-only = %v, want val", result["base-only"])
	}
}

func TestDeepMerge_ChildOnlyKey(t *testing.T) {
	result := deepMerge(map[string]any{}, map[string]any{"child-only": "val"})
	if result["child-only"] != "val" {
		t.Errorf("child-only = %v, want val", result["child-only"])
	}
}

// --- mergeIntoFilesystem ---

func TestMergeIntoFilesystem_ExistingEntries(t *testing.T) {
	v := map[string]any{
		"filesystem": map[string]any{
			"allow": []any{"~/.claude"},
		},
	}
	mergeIntoFilesystem(v, []string{"./bin", "./src"}, []string{"./go.mod"})
	fs, ok := v["filesystem"].(map[string]any)
	if !ok {
		t.Fatal("filesystem not a map")
	}
	allow, _ := fs["allow"].([]any)
	if !slicesEqual(allow, []any{"~/.claude", "./bin", "./src"}) {
		t.Errorf("allow = %v, want [~/.claude ./bin ./src]", allow)
	}
	allowFile, _ := fs["allow_file"].([]any)
	if !slicesEqual(allowFile, []any{"./go.mod"}) {
		t.Errorf("allow_file = %v, want [./go.mod]", allowFile)
	}
}

func TestMergeIntoFilesystem_NoFilesystemKey(t *testing.T) {
	v := map[string]any{}
	mergeIntoFilesystem(v, []string{"./bin"}, []string{"./go.mod"})
	fs, ok := v["filesystem"].(map[string]any)
	if !ok {
		t.Fatal("filesystem not created")
	}
	if allow, _ := fs["allow"].([]any); !slicesEqual(allow, []any{"./bin"}) {
		t.Errorf("allow = %v, want [./bin]", allow)
	}
	if allowFile, _ := fs["allow_file"].([]any); !slicesEqual(allowFile, []any{"./go.mod"}) {
		t.Errorf("allow_file = %v, want [./go.mod]", allowFile)
	}
}

func TestMergeIntoFilesystem_EmptyInput(t *testing.T) {
	v := map[string]any{"filesystem": map[string]any{"allow": []any{"~/.claude"}}}
	mergeIntoFilesystem(v, nil, nil)
	fs, _ := v["filesystem"].(map[string]any)
	allow, _ := fs["allow"].([]any)
	if !slicesEqual(allow, []any{"~/.claude"}) {
		t.Errorf("allow = %v, want [~/.claude]", allow)
	}
	if fs["allow_file"] != nil {
		t.Errorf("allow_file = %v, want nil", fs["allow_file"])
	}
}

// --- setWorkdirAccess ---

func TestSetWorkdirAccess_Override(t *testing.T) {
	v := map[string]any{"workdir": map[string]any{"access": "write"}}
	setWorkdirAccess(v)
	workdir, _ := v["workdir"].(map[string]any)
	if workdir["access"] != "read" {
		t.Errorf("access = %v, want read", workdir["access"])
	}
}

func TestSetWorkdirAccess_NoWorkdirKey(t *testing.T) {
	v := map[string]any{}
	setWorkdirAccess(v)
	workdir, ok := v["workdir"].(map[string]any)
	if !ok {
		t.Fatal("workdir not created")
	}
	if workdir["access"] != "read" {
		t.Errorf("access = %v, want read", workdir["access"])
	}
}

// --- extractExtension ---

func TestExtractExtension_WithDenyWrite(t *testing.T) {
	v := map[string]any{
		"extension": map[string]any{
			"deny_write": []any{".git", "node_modules"},
		},
		"meta": map[string]any{"name": "base"},
	}
	ext := extractExtension(v)
	if !slicesEqual(toAny(ext.denyWrite), []any{".git", "node_modules"}) {
		t.Errorf("denyWrite = %v, want [.git node_modules]", ext.denyWrite)
	}
	if ext.base != "" {
		t.Errorf("base = %q, want empty", ext.base)
	}
	if _, exists := v["extension"]; exists {
		t.Error("extension key should be removed from v")
	}
}

func TestExtractExtension_NoExtension(t *testing.T) {
	v := map[string]any{"meta": map[string]any{"name": "base"}}
	ext := extractExtension(v)
	if ext.denyWrite != nil {
		t.Errorf("denyWrite = %v, want nil", ext.denyWrite)
	}
	if ext.base != "" {
		t.Errorf("base = %q, want empty", ext.base)
	}
}

func TestExtractExtension_NoDenyWrite(t *testing.T) {
	v := map[string]any{"extension": map[string]any{}}
	ext := extractExtension(v)
	if ext.denyWrite != nil {
		t.Errorf("denyWrite = %v, want nil", ext.denyWrite)
	}
}

func TestExtractExtension_WithBase(t *testing.T) {
	v := map[string]any{
		"extension": map[string]any{
			"base": "base.toml",
		},
	}
	ext := extractExtension(v)
	if ext.base != "base.toml" {
		t.Errorf("base = %q, want base.toml", ext.base)
	}
}

// --- GenerateProfile ---

func TestGenerateProfile_Simple(t *testing.T) {
	tmp := t.TempDir()
	tomlContent := `
[meta]
name = "test"

[environment]
allow_vars = ["HOME"]
`
	tomlPath := filepath.Join(tmp, "test.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	out, err := GenerateProfile(tomlPath, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	workdir, ok := got["workdir"].(map[string]any)
	if !ok {
		t.Fatal("workdir not in output")
	}
	if workdir["access"] != "read" {
		t.Errorf("workdir.access = %v, want read", workdir["access"])
	}

	fs, ok := got["filesystem"].(map[string]any)
	if !ok {
		t.Fatal("filesystem not in output")
	}
	allow, _ := fs["allow"].([]any)
	if !containsStr(allow, "./subdir") {
		t.Errorf("./subdir not found in filesystem.allow: %v", allow)
	}
}

func TestGenerateProfile_WithBase(t *testing.T) {
	tmp := t.TempDir()
	baseContent := `
[meta]
name = "base"

[filesystem]
allow = ["~/.claude"]
`
	childContent := `
[extension]
base = "base.toml"
deny_write = ["child.toml"]

[meta]
name = "child"
`
	if err := os.WriteFile(filepath.Join(tmp, "base.toml"), []byte(baseContent), 0644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(tmp, "child.toml")
	if err := os.WriteFile(childPath, []byte(childContent), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := GenerateProfile(childPath, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	fs, ok := got["filesystem"].(map[string]any)
	if !ok {
		t.Fatal("filesystem not in output")
	}
	allowFile, _ := fs["allow_file"].([]any)
	for _, v := range allowFile {
		if v == "./child.toml" {
			t.Error("child.toml should be excluded by deny_write")
		}
	}

	allow, _ := fs["allow"].([]any)
	if !containsStr(allow, "~/.claude") {
		t.Errorf("~/.claude not in filesystem.allow (base merge failed): %v", allow)
	}

	meta, ok := got["meta"].(map[string]any)
	if !ok {
		t.Fatal("meta not in output")
	}
	if meta["name"] != "child" {
		t.Errorf("meta.name = %v, want child", meta["name"])
	}
}

func TestGenerateProfile_MissingToml(t *testing.T) {
	_, err := GenerateProfile("/nonexistent/nono.toml", t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing TOML, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected error to wrap os.ErrNotExist, got: %v", err)
	}
}

// --- helpers ---

func elementsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}

func slicesEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(slice []any, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
