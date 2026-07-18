package router

import "testing"

func TestIsGhInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bare gh", []string{"gh"}, true},
		{"gh subcommand", []string{"gh", "pr", "view"}, true},
		{"gh api", []string{"gh", "api", "/repos"}, true},
		{"prefix ghi", []string{"ghi", "status"}, false},
		{"prefix github", []string{"github"}, false},
		{"other command", []string{"git", "status"}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isGhInvocation(Segment{Args: c.args})
			if got != c.want {
				t.Errorf("isGhInvocation(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}

func TestContainsGhCommand(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"command substitution", "echo $(gh pr view 42)", true},
		{"backticks", "echo `gh pr view 42`", true},
		{"backgrounded", "gh pr view 42 &", true},
		{"leading gh with sub elsewhere", "gh pr list & echo $(date)", true},
		{"pipe then gh in sub", "cat x | echo $(gh api /repos)", true},
		{"github prefix not matched", "echo $(github --version)", false},
		{"gh inside word", "echo longhorn &", false},
		{"gh inside high", "echo high &", false},
		{"quoted gh word not command", `echo "gh" &`, false},
		{"no gh at all", "echo $(date) &", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := containsGhCommand(c.raw)
			if got != c.want {
				t.Errorf("containsGhCommand(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}
