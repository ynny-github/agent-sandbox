package shellquote_test

import (
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/shellquote"
)

func TestQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "echo hi", `'echo hi'`},
		{"empty", "", `''`},
		{"embedded single quote", "git commit -m 'hi there'", `'git commit -m '\''hi there'\'''`},
		{"pipe and redirect preserved literally", "cat a | grep b > c", `'cat a | grep b > c'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellquote.Quote(tc.in); got != tc.want {
				t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
