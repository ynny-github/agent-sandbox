package gitutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/gitutil"
)

func TestDetectWorktreeGitDir_RegularRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	got, ok := gitutil.DetectWorktreeGitDir(dir)
	if ok {
		t.Errorf("ok = true, want false; got = %q", got)
	}
}

func TestDetectWorktreeGitDir_ValidWorktree(t *testing.T) {
	base := t.TempDir()
	mainGit := filepath.Join(base, "repo", ".git")
	if err := os.MkdirAll(mainGit, 0755); err != nil {
		t.Fatal(err)
	}
	gitdir := filepath.Join(mainGit, "worktrees", "feat")

	worktreeDir := filepath.Join(base, "worktree")
	if err := os.Mkdir(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "gitdir: " + gitdir + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, ok := gitutil.DetectWorktreeGitDir(worktreeDir)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != mainGit {
		t.Errorf("got = %q, want %q", got, mainGit)
	}
}

func TestDetectWorktreeGitDir_RelativeGitdir(t *testing.T) {
	base := t.TempDir()
	mainGit := filepath.Join(base, "repo", ".git")
	if err := os.MkdirAll(mainGit, 0755); err != nil {
		t.Fatal(err)
	}

	worktreeDir := filepath.Join(base, "worktree")
	if err := os.Mkdir(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	// relative from worktreeDir: ../repo/.git/worktrees/feat
	relGitdir := "../repo/.git/worktrees/feat"
	content := "gitdir: " + relGitdir + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, ok := gitutil.DetectWorktreeGitDir(worktreeDir)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != mainGit {
		t.Errorf("got = %q, want %q", got, mainGit)
	}
}

func TestDetectWorktreeGitDir_MalformedGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, ok := gitutil.DetectWorktreeGitDir(dir)
	if ok {
		t.Error("ok = true, want false")
	}
}

func TestDetectWorktreeGitDir_NoDotGit(t *testing.T) {
	dir := t.TempDir()
	_, ok := gitutil.DetectWorktreeGitDir(dir)
	if ok {
		t.Error("ok = true, want false")
	}
}

func TestDetectWorktreeGitDir_ShallowGitdir(t *testing.T) {
	dir := t.TempDir()
	// gitdir = "/foo" → filepath.Dir(filepath.Dir("/foo")) = "/"  → must return false
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, ok := gitutil.DetectWorktreeGitDir(dir)
	if ok {
		t.Error("ok = true, want false for shallow gitdir")
	}
}
