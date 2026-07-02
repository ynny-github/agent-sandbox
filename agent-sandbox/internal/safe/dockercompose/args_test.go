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
		{
			name:       "boolean global flag before subcommand",
			args:       []string{"--dry-run", "up"},
			wantGlobal: []string{"--dry-run"},
			wantSub:    "up",
			wantRest:   []string{"up"},
		},
		{
			name:       "boolean global flag does not hide run",
			args:       []string{"--dry-run", "run", "web", "sh"},
			wantGlobal: []string{"--dry-run"},
			wantSub:    "run",
			wantRest:   []string{"run", "web", "sh"},
		},
		{
			name:       "short value flag with attached value",
			args:       []string{"-fcompose.yml", "up"},
			wantGlobal: []string{"-fcompose.yml"},
			wantSub:    "up",
			wantRest:   []string{"up"},
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
			if got.Unrecognized != "" {
				t.Errorf("Unrecognized = %q, want empty", got.Unrecognized)
			}
		})
	}
}

func TestParseArgs_UnrecognizedLeadingFlag(t *testing.T) {
	got := dockercompose.ParseArgs([]string{"--bogus", "value", "run", "web"})
	if got.Unrecognized != "--bogus" {
		t.Errorf("Unrecognized = %q, want %q", got.Unrecognized, "--bogus")
	}
}

func TestParseArgs_HelpRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"global long help", []string{"--help"}, true},
		{"global short help", []string{"-h"}, true},
		{"help then subcommand", []string{"--help", "up"}, true},
		{"help after global flags", []string{"-f", "x.yml", "--help"}, true},
		{"no help", []string{"up", "-d"}, false},
		{"help after subcommand is not global", []string{"up", "--help"}, false},
		{"help swallowed as flag value", []string{"--project-name", "--help", "up"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dockercompose.ParseArgs(tc.args).HelpRequested; got != tc.want {
				t.Errorf("HelpRequested = %v, want %v", got, tc.want)
			}
			if got := dockercompose.WantsGlobalHelp(tc.args); got != tc.want {
				t.Errorf("WantsGlobalHelp = %v, want %v", got, tc.want)
			}
		})
	}
}
