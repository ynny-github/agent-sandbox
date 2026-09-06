package cmd

import "testing"

// The safe parent command and the docker wrapper must be registered so
// `agent-sandbox safe docker` resolves.
func TestSafeDockerCommand_Registered(t *testing.T) {
	safe, _, err := rootCmd.Find([]string{"safe"})
	if err != nil || safe.Name() != "safe" {
		t.Fatalf("safe command not found: %v", err)
	}
	cmd, _, err := rootCmd.Find([]string{"safe", "docker"})
	if err != nil {
		t.Fatalf("docker command not found: %v", err)
	}
	if cmd.Name() != "docker" {
		t.Errorf("got %q, want docker", cmd.Name())
	}
	if !cmd.DisableFlagParsing {
		t.Error("docker must disable flag parsing to pass args verbatim")
	}
}

func TestSplitDockerGlobal(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		sub          string
		rest         []string
		unrecognized string
	}{
		{"empty", nil, "", nil, ""},
		{"plain subcommand", []string{"run", "--rm", "alpine"}, "run", []string{"run", "--rm", "alpine"}, ""},
		{"compose immediate", []string{"compose", "up", "-d"}, "compose", []string{"compose", "up", "-d"}, ""},
		{"global bool then compose", []string{"--debug", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"global value separate then compose", []string{"--context", "foo", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"global value attached then compose", []string{"--context=foo", "compose", "up"}, "compose", []string{"compose", "up"}, ""},
		{"short value flag then compose", []string{"-H", "unix:///var/run/docker.sock", "compose", "config"}, "compose", []string{"compose", "config"}, ""},
		{"help flag alone", []string{"--help"}, "", nil, ""},
		{"unrecognized global flag", []string{"--bogus", "compose", "up"}, "", nil, "--bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, rest, unrecognized := splitDockerGlobal(tc.args)
			if sub != tc.sub {
				t.Errorf("sub = %q, want %q", sub, tc.sub)
			}
			if unrecognized != tc.unrecognized {
				t.Errorf("unrecognized = %q, want %q", unrecognized, tc.unrecognized)
			}
			if len(rest) != len(tc.rest) {
				t.Fatalf("rest = %v, want %v", rest, tc.rest)
			}
			for i := range rest {
				if rest[i] != tc.rest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, rest[i], tc.rest[i])
				}
			}
		})
	}
}
