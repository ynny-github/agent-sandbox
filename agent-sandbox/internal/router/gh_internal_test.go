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
