package config

import (
	"path/filepath"
	"testing"
)

func TestUserConfigPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	got, err := userConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/home/tester", ".config", "agent-sandbox", "config.toml")
	if got != want {
		t.Errorf("userConfigPath() = %q, want %q", got, want)
	}
}

func TestUserConfigPath_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := userConfigPath(); err == nil {
		t.Error("userConfigPath() error = nil, want error when HOME is empty")
	}
}
