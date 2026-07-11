package gh_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/gh"
)

func eqStrs(a, b []string) bool {
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

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		command string
		sub     string
		args    []string
		globals []string
	}{
		{"empty", nil, "", "", nil, nil},
		{"command only", []string{"pr"}, "pr", "", nil, nil},
		{"command and verb", []string{"pr", "list"}, "pr", "list", nil, nil},
		{"verb with args", []string{"pr", "view", "5", "--json", "title"}, "pr", "view",
			[]string{"5", "--json", "title"}, nil},
		{"hyphenated project verb", []string{"project", "item-list", "3"}, "project", "item-list",
			[]string{"3"}, nil},
		{"leading option kept", []string{"--version"}, "", "", nil, []string{"--version"}},
		{"help flag after command", []string{"pr", "--help"}, "pr", "", nil, nil},
		{"option before verb is skipped to find verb", []string{"pr", "-R", "o/x", "merge", "5"},
			"pr", "o/x", []string{"merge", "5"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := gh.Parse(tc.argv)
			if inv.Command != tc.command {
				t.Errorf("Command = %q, want %q", inv.Command, tc.command)
			}
			if inv.Subcommand != tc.sub {
				t.Errorf("Subcommand = %q, want %q", inv.Subcommand, tc.sub)
			}
			if !eqStrs(inv.Args, tc.args) {
				t.Errorf("Args = %v, want %v", inv.Args, tc.args)
			}
			if !eqStrs(inv.Global, tc.globals) {
				t.Errorf("Global = %v, want %v", inv.Global, tc.globals)
			}
		})
	}
}
