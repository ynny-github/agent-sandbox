package cmd

import "testing"

func TestRunDebug_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	if err := runDebug(debugCmd, nil); err == nil {
		t.Fatal("expected config error, got nil")
	}
}
