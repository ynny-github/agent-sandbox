package cmd

import "testing"

func subcommandNames(t *testing.T) map[string]bool {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "sandbox" {
			names := make(map[string]bool)
			for _, sub := range c.Commands() {
				names[sub.Name()] = true
			}
			return names
		}
	}
	t.Fatal("sandbox parent command is not registered on rootCmd")
	return nil
}

func TestSandboxParentHasSubcommands(t *testing.T) {
	names := subcommandNames(t)
	for _, want := range []string{"up", "down", "prune"} {
		if !names[want] {
			t.Fatalf("sandbox subcommand %q is not registered", want)
		}
	}
}

func TestSandboxUpHasDetachFlag(t *testing.T) {
	flag := sandboxUpCmd.Flags().Lookup("detach")
	if flag == nil {
		t.Fatal("sandbox up missing --detach flag")
	}
	if flag.Shorthand != "d" {
		t.Fatalf("--detach shorthand = %q, want d", flag.Shorthand)
	}
}
