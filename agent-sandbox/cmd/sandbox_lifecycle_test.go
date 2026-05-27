package cmd

import "testing"

func TestSandboxUpHasDetachFlag(t *testing.T) {
	flag := sandboxUpCmd.Flags().Lookup("detach")
	if flag == nil {
		t.Fatal("sandbox-up missing --detach flag")
	}
	if flag.Shorthand != "d" {
		t.Fatalf("--detach shorthand = %q, want d", flag.Shorthand)
	}
}

func TestSandboxDownRegistered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "sandbox-down" {
			return
		}
	}
	t.Fatal("sandbox-down command is not registered")
}
