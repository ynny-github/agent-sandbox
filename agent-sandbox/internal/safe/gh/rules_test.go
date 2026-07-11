package gh_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/gh"
)

// reason returns the refusal message for argv, or "" when allowed.
func reason(argv []string) string {
	vs := gh.Check(argv)
	if len(vs) == 0 {
		return ""
	}
	return vs[0].Setting
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string // substring the refusal must contain, or "" when allowed
	}{
		// allowed reads
		{"pr list", []string{"pr", "list"}, ""},
		{"pr view", []string{"pr", "view", "5"}, ""},
		{"pr diff", []string{"pr", "diff", "5"}, ""},
		{"pr checks", []string{"pr", "checks"}, ""},
		{"issue list", []string{"issue", "list"}, ""},
		{"issue view", []string{"issue", "view", "3"}, ""},
		{"project list", []string{"project", "list"}, ""},
		{"project item-list", []string{"project", "item-list", "1"}, ""},
		{"project field-list", []string{"project", "field-list", "1"}, ""},

		// allowed non-destructive writes
		{"pr create", []string{"pr", "create", "--fill"}, ""},
		{"pr edit", []string{"pr", "edit", "5", "--add-label", "bug"}, ""},
		{"pr comment", []string{"pr", "comment", "5", "-b", "hi"}, ""},
		{"pr reopen", []string{"pr", "reopen", "5"}, ""},
		{"pr ready", []string{"pr", "ready", "5"}, ""},
		{"pr review", []string{"pr", "review", "5", "--approve"}, ""},
		{"issue create", []string{"issue", "create", "--title", "x"}, ""},
		{"issue edit", []string{"issue", "edit", "3", "--add-label", "bug"}, ""},
		{"issue reopen", []string{"issue", "reopen", "3"}, ""},
		{"issue pin", []string{"issue", "pin", "3"}, ""},
		{"project item-add", []string{"project", "item-add", "1", "--url", "x"}, ""},
		{"project item-edit", []string{"project", "item-edit", "--id", "x"}, ""},

		// blocked destructive verbs (in scope)
		{"pr merge", []string{"pr", "merge", "5"}, "not permitted"},
		{"pr close", []string{"pr", "close", "5"}, "not permitted"},
		{"pr checkout", []string{"pr", "checkout", "5"}, "not permitted"},
		{"pr update-branch", []string{"pr", "update-branch", "5"}, "not permitted"},
		{"issue delete", []string{"issue", "delete", "3"}, "not permitted"},
		{"issue close", []string{"issue", "close", "3"}, "not permitted"},
		{"issue transfer", []string{"issue", "transfer", "3", "o/r"}, "not permitted"},
		{"issue develop", []string{"issue", "develop", "3"}, "not permitted"},
		{"project delete", []string{"project", "delete", "1"}, "not permitted"},
		{"project item-delete", []string{"project", "item-delete", "--id", "x"}, "not permitted"},
		{"project field-delete", []string{"project", "field-delete", "--id", "x"}, "not permitted"},

		// unknown verb on an in-scope resource is refused (fail closed)
		{"unknown pr verb", []string{"pr", "obliterate", "5"}, "not permitted"},

		// gh api is refused with its dedicated message
		{"api get", []string{"api", "/repos/o/r"}, "gh api"},
		{"api delete", []string{"api", "-X", "DELETE", "/repos/o/r"}, "gh api"},

		// out-of-scope resources are refused
		{"repo delete", []string{"repo", "delete", "o/r"}, "outside the safe scope"},
		{"secret list", []string{"secret", "list"}, "outside the safe scope"},
		{"auth token", []string{"auth", "token"}, "outside the safe scope"},
		{"unknown command", []string{"frobnicate", "x"}, "outside the safe scope"},

		// help passthrough
		{"bare command help", []string{"pr"}, ""},
		{"verb help flag", []string{"pr", "merge", "--help"}, ""},
		{"short help flag", []string{"issue", "delete", "-h"}, ""},
		{"version", []string{"--version"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reason(tc.argv)
			if tc.want == "" {
				if got != "" {
					t.Errorf("Check(%v) refused with %q, want allowed", tc.argv, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("Check(%v) = %q, want it to contain %q", tc.argv, got, tc.want)
			}
		})
	}
}

func TestCheck_ViolationShape(t *testing.T) {
	vs := gh.Check([]string{"pr", "merge", "5"})
	if len(vs) != 1 {
		t.Fatalf("got %d violations, want 1", len(vs))
	}
	if vs[0].Source != "cli" {
		t.Errorf("Source = %q, want \"cli\"", vs[0].Source)
	}
	if vs[0].Setting == "" {
		t.Error("Setting is empty, want a reason message")
	}
}
