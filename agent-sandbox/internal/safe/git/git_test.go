package git_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/git"
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

func eqGlobals(a, b []git.GlobalOpt) bool {
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
		sub     string
		args    []string
		globals []git.GlobalOpt
	}{
		{"empty", nil, "", nil, nil},
		{"simple subcommand", []string{"status"}, "status", nil, nil},
		{"flags after subcommand", []string{"push", "--force"}, "push", []string{"--force"}, nil},
		{"global -c separate", []string{"-c", "core.hooksPath=/dev/null", "push"}, "push", nil,
			[]git.GlobalOpt{{Name: "-c", Value: "core.hooksPath=/dev/null"}}},
		{"global -C separate then sub", []string{"-C", "/repo", "commit", "-m", "x"}, "commit",
			[]string{"-m", "x"}, []git.GlobalOpt{{Name: "-C", Value: "/repo"}}},
		{"global --git-dir attached", []string{"--git-dir=/x", "log"}, "log", nil,
			[]git.GlobalOpt{{Name: "--git-dir", Value: "/x"}}},
		{"global bool paginate", []string{"-p", "log"}, "log", nil,
			[]git.GlobalOpt{{Name: "-p"}}},
		{"two globals then sub", []string{"-c", "a=b", "-C", "/r", "reset", "--hard"}, "reset",
			[]string{"--hard"}, []git.GlobalOpt{{Name: "-c", Value: "a=b"}, {Name: "-C", Value: "/r"}}},
		{"unknown global kept, subcommand found", []string{"--no-advice", "push", "--force"}, "push",
			[]string{"--force"}, []git.GlobalOpt{{Name: "--no-advice"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := git.Parse(tc.argv)
			if inv.Subcommand != tc.sub {
				t.Errorf("Subcommand = %q, want %q", inv.Subcommand, tc.sub)
			}
			if !eqStrs(inv.Args, tc.args) {
				t.Errorf("Args = %v, want %v", inv.Args, tc.args)
			}
			if !eqGlobals(inv.Global, tc.globals) {
				t.Errorf("Global = %v, want %v", inv.Global, tc.globals)
			}
		})
	}
}
