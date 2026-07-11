package git_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/git"
)

// matchedIDs returns the IDs of rules that fire for argv.
func matchedIDs(argv []string) []string {
	inv := git.Parse(argv)
	var ids []string
	for _, r := range git.Rules() {
		if r.Match(inv) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

func hasID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestRules(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		blocked string // rule ID expected to fire, or "" for allowed
	}{
		// force-push and its safe exception
		{"force push long", []string{"push", "--force"}, "force-push"},
		{"force push short", []string{"push", "-f"}, "force-push"},
		{"force-with-lease allowed", []string{"push", "--force-with-lease"}, ""},
		{"force-if-includes allowed", []string{"push", "--force-if-includes"}, ""},
		{"push delete", []string{"push", "origin", "--delete", "topic"}, "force-push"},
		{"push refspec delete", []string{"push", "origin", ":topic"}, "force-push"},
		{"plain push allowed", []string{"push", "origin", "main"}, ""},

		// reset
		{"hard reset", []string{"reset", "--hard"}, "hard-reset"},
		{"soft reset allowed", []string{"reset", "--soft", "HEAD~1"}, ""},

		// clean
		{"clean force", []string{"clean", "-fd"}, "clean-force"},
		{"clean dry-run allowed", []string{"clean", "-nd"}, ""},

		// branch
		{"branch force delete cap", []string{"branch", "-D", "topic"}, "branch-force-delete"},
		{"branch delete allowed", []string{"branch", "-d", "topic"}, ""},

		// history rewrite
		{"filter-branch", []string{"filter-branch", "--tree-filter", "x"}, "filter-history"},
		{"filter-repo", []string{"filter-repo", "--path", "x"}, "filter-history"},
		{"update-ref delete", []string{"update-ref", "-d", "refs/heads/x"}, "update-ref-delete"},
		{"reflog expire", []string{"reflog", "expire", "--all"}, "reflog-expire"},
		{"gc prune now", []string{"gc", "--prune=now"}, "gc-prune"},

		// hook / signature bypass
		{"no-verify", []string{"commit", "--no-verify", "-m", "x"}, "bypass-hooks"},
		{"commit -n", []string{"commit", "-n", "-m", "x"}, "bypass-hooks"},
		{"no-gpg-sign", []string{"commit", "--no-gpg-sign", "-m", "x"}, "bypass-hooks"},
		{"c hookspath", []string{"-c", "core.hooksPath=/dev/null", "push"}, "bypass-hooks"},
		{"push dry-run n allowed", []string{"push", "-n"}, ""},

		// regression: bypasses found in whole-branch review
		{"force-if-includes plus force blocked", []string{"push", "--force-if-includes", "--force"}, "force-push"},
		{"scoped lease plus force blocked", []string{"push", "--force-with-lease=other", "--force", "origin", "main"}, "force-push"},
		{"lease plus delete blocked", []string{"push", "--force-with-lease", "--delete", "topic"}, "force-push"},
		{"config-env hookspath blocked", []string{"--config-env=core.hooksPath=HP", "commit", "-m", "x"}, "bypass-hooks"},
		{"gpgsign zero blocked", []string{"-c", "commit.gpgsign=0", "commit", "-m", "x"}, "bypass-hooks"},
		{"gpgsign off blocked", []string{"-c", "commit.gpgsign=off", "commit", "-m", "x"}, "bypass-hooks"},
		{"alias injection blocked", []string{"-c", "alias.x=push --force", "x"}, "alias-injection"},

		// stash / remote / tag
		{"stash drop", []string{"stash", "drop"}, "stash-destroy"},
		{"stash clear", []string{"stash", "clear"}, "stash-destroy"},
		{"stash pop allowed", []string{"stash", "pop"}, ""},
		{"remote remove", []string{"remote", "remove", "origin"}, "remote-tamper"},
		{"remote set-url", []string{"remote", "set-url", "origin", "x"}, "remote-tamper"},
		{"remote add allowed", []string{"remote", "add", "origin", "x"}, ""},
		{"tag delete", []string{"tag", "-d", "v1"}, "tag-delete"},
		{"tag create allowed", []string{"tag", "v1"}, ""},

		// discard changes
		{"checkout discard path", []string{"checkout", "--", "file.go"}, "discard-changes"},
		{"checkout dot", []string{"checkout", "."}, "discard-changes"},
		{"checkout branch allowed", []string{"checkout", "topic"}, ""},
		{"restore worktree", []string{"restore", "file.go"}, "discard-changes"},
		{"restore staged allowed", []string{"restore", "--staged", "file.go"}, ""},

		// config
		{"config local write", []string{"config", "user.name", "x"}, "config-write"},
		{"config global write", []string{"config", "--global", "user.name", "x"}, "config-write"},
		{"config unset", []string{"config", "--unset", "user.name"}, "config-write"},
		{"config get allowed", []string{"config", "--get", "user.name"}, ""},
		{"config list allowed", []string{"config", "--list"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := matchedIDs(tc.argv)
			if tc.blocked == "" {
				if len(ids) != 0 {
					t.Errorf("argv %v: expected allowed, got rules %v", tc.argv, ids)
				}
				return
			}
			if !hasID(ids, tc.blocked) {
				t.Errorf("argv %v: expected rule %q, got %v", tc.argv, tc.blocked, ids)
			}
		})
	}
}

func TestCheck_ReturnsCliViolations(t *testing.T) {
	vs := git.Check([]string{"push", "--force"})
	if len(vs) == 0 {
		t.Fatal("expected a violation for push --force")
	}
	if vs[0].Source != "cli" {
		t.Errorf("Source = %q, want cli", vs[0].Source)
	}
	if !strings.Contains(vs[0].Setting, "force") {
		t.Errorf("Setting = %q, want it to mention force", vs[0].Setting)
	}
}

func TestCheck_CleanReturnsNil(t *testing.T) {
	if vs := git.Check([]string{"status"}); len(vs) != 0 {
		t.Errorf("git status: expected no violations, got %v", vs)
	}
}
