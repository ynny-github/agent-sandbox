package gitutil

import (
	"os"
	"path/filepath"
	"strings"
)

// DetectWorktreeGitDir returns the main .git directory path when cwd is a
// git worktree, and ("", false) otherwise or on any error.
func DetectWorktreeGitDir(cwd string) (string, bool) {
	dotGit := filepath.Join(cwd, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	gitdir := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(cwd, gitdir)
	}
	// gitdir = <main>/.git/worktrees/<name> → up 2 levels = <main>/.git
	mainGit := filepath.Dir(filepath.Dir(filepath.Clean(gitdir)))
	if mainGit == "." || mainGit == "/" {
		return "", false
	}
	if si, err := os.Stat(mainGit); err != nil || !si.IsDir() {
		return "", false
	}
	return mainGit, true
}
