package cmd

import "testing"

// The safe parent command and the docker-compose wrapper must be registered so
// `agent-sandbox safe docker-compose` resolves.
func TestSafeDockerComposeCommand_Registered(t *testing.T) {
	safe, _, err := rootCmd.Find([]string{"safe"})
	if err != nil || safe.Name() != "safe" {
		t.Fatalf("safe command not found: %v", err)
	}
	cmd, _, err := rootCmd.Find([]string{"safe", "docker-compose"})
	if err != nil {
		t.Fatalf("docker-compose command not found: %v", err)
	}
	if cmd.Name() != "docker-compose" {
		t.Errorf("got %q, want docker-compose", cmd.Name())
	}
	if !cmd.DisableFlagParsing {
		t.Error("docker-compose must disable flag parsing to pass args verbatim")
	}
}
