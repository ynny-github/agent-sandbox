package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestDedupUnion(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"both empty", nil, nil, nil},
		{"a only", []string{"x", "y"}, nil, []string{"x", "y"}},
		{"b only", nil, []string{"x"}, []string{"x"}},
		{"disjoint", []string{"a"}, []string{"b"}, []string{"a", "b"}},
		{"overlap deduped", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"identical", []string{"a"}, []string{"a"}, []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupUnion(tt.a, tt.b)
			if !slices.Equal(got, tt.want) {
				t.Errorf("dedupUnion(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

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
