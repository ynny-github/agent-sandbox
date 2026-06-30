package dockercompose_test

import (
	"reflect"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe/dockercompose"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantGlobal []string
		wantSub    string
		wantRest   []string
	}{
		{
			name:     "subcommand only",
			args:     []string{"up", "-d"},
			wantSub:  "up",
			wantRest: []string{"up", "-d"},
		},
		{
			name:       "file flag with value before subcommand",
			args:       []string{"-f", "compose.yml", "up"},
			wantGlobal: []string{"-f", "compose.yml"},
			wantSub:    "up",
			wantRest:   []string{"up"},
		},
		{
			name:       "long flags and profile",
			args:       []string{"--profile", "dev", "--project-directory", "/p", "config"},
			wantGlobal: []string{"--profile", "dev", "--project-directory", "/p"},
			wantSub:    "config",
			wantRest:   []string{"config"},
		},
		{
			name:       "equals form attaches value",
			args:       []string{"--file=compose.yml", "run", "web"},
			wantGlobal: []string{"--file=compose.yml"},
			wantSub:    "run",
			wantRest:   []string{"run", "web"},
		},
		{
			name: "no args",
			args: nil,
		},
		{
			name:       "global flags only no subcommand",
			args:       []string{"-f", "compose.yml"},
			wantGlobal: []string{"-f", "compose.yml"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dockercompose.ParseArgs(tc.args)
			if !reflect.DeepEqual(got.GlobalFlags, tc.wantGlobal) {
				t.Errorf("GlobalFlags = %v, want %v", got.GlobalFlags, tc.wantGlobal)
			}
			if got.Subcommand != tc.wantSub {
				t.Errorf("Subcommand = %q, want %q", got.Subcommand, tc.wantSub)
			}
			if !reflect.DeepEqual(got.Rest, tc.wantRest) {
				t.Errorf("Rest = %v, want %v", got.Rest, tc.wantRest)
			}
		})
	}
}
