package gitutil

import (
	"os"
	"path/filepath"
	"strings"
)

// DetectWorktreeGitDir returns the main .git directory path when cwd (or any
// of its ancestors) is a git worktree, and ("", false) otherwise or on any error.
func DetectWorktreeGitDir(cwd string) (string, bool) {
	dir := cwd
	for {
		dotGit := filepath.Join(dir, ".git")
		fi, err := os.Stat(dotGit)
		if err == nil {
			if !fi.Mode().IsRegular() {
				// .git is a directory → ordinary repo, not a worktree
				return "", false
			}
			return parseWorktreeGitFile(dotGit, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func parseWorktreeGitFile(dotGit, dir string) (string, bool) {
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
		gitdir = filepath.Join(dir, gitdir)
	}
	// gitdir = <main>/.git/worktrees/<name> → up 2 levels = <main>/.git
	mainGit := filepath.Dir(filepath.Dir(filepath.Clean(gitdir)))
	if mainGit == "." || mainGit == "/" {
		return "", false
	}
	return mainGit, true
}
