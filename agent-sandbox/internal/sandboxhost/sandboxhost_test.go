package sandboxhost

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func resolve(t *testing.T, h config.HostConfig, agent string) *Resolved {
	t.Helper()
	cfg := &config.Config{}
	cfg.Sandbox.Host = h
	r, err := Resolve(cfg, agent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

func profileMap(t *testing.T, r *Resolved) map[string]any {
	t.Helper()
	data, err := r.ProfileJSON()
	if err != nil {
		t.Fatalf("ProfileJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestResolve_Parity reproduces the retired nono.jsonc from the migrated
// [sandbox.host] and asserts the generated profile, modulo the two intentional
// deltas (taskgate path, no gh).
func TestResolve_Parity(t *testing.T) {
	r := resolve(t, config.HostConfig{
		Capabilities: []string{"go", "python", "docker", "ssh", "mise", "taskgate"},
	}, "claude")

	want := map[string]any{
		"extends": "claude",
		"meta":    map[string]any{"name": "custom claude"},
		"groups":  map[string]any{"include": []any{"go_runtime", "python_runtime"}},
		"filesystem": map[string]any{
			"read": []any{
				"~/.docker", "~/.local/share/mise", "~/.local/state/taskgate",
				"~/.orbstack", "~/.ssh",
			},
			"allow_file":        []any{"/dev/null", "~/.ssh/known_hosts"},
			"bypass_protection": []any{"~/.docker", "~/.ssh"},
		},
		"environment": map[string]any{
			"allow_vars": []any{"HOME", "LANG", "LC_ALL", "MISE*", "PATH", "TERM", "USER", "__MISE*"},
		},
	}
	if got := profileMap(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("profile mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestResolve_DenyRulesFromCapabilities(t *testing.T) {
	r := resolve(t, config.HostConfig{Capabilities: []string{"ssh", "docker"}}, "claude")
	want := []string{
		"Edit(~/.ssh/known_hosts)",
		"Glob(~/.docker/**)", "Glob(~/.ssh/**)",
		"Grep(~/.docker/**)", "Grep(~/.ssh/**)",
		"Read(~/.docker/**)", "Read(~/.ssh/**)",
		"Write(~/.ssh/known_hosts)",
	}
	if !reflect.DeepEqual(r.DenyRules, want) {
		t.Errorf("DenyRules = %v\nwant %v", r.DenyRules, want)
	}
}

func TestResolve_BaselineOnlyWhenEmpty(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{}, "claude"))
	env := m["environment"].(map[string]any)["allow_vars"].([]any)
	if !reflect.DeepEqual(env, []any{"HOME", "LANG", "LC_ALL", "PATH", "TERM", "USER"}) {
		t.Errorf("baseline env = %v", env)
	}
	fs := m["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["allow_file"], []any{"/dev/null"}) {
		t.Errorf("baseline allow_file = %v", fs["allow_file"])
	}
	if m["extends"] != "claude" || m["groups"] != nil {
		t.Errorf("expected extends=claude and no groups; got %#v", m)
	}
}

func TestResolve_UnknownCapability(t *testing.T) {
	_, err := Resolve(hostCfg(config.HostConfig{Capabilities: []string{"rust"}}), "claude")
	if err == nil || !strings.Contains(err.Error(), "rust") {
		t.Fatalf("expected unknown-capability error naming rust, got %v", err)
	}
}

func TestResolve_UnknownAgent(t *testing.T) {
	if _, err := Resolve(hostCfg(config.HostConfig{}), "codex"); err == nil {
		t.Fatal("expected unknown-agent error, got nil")
	}
}

func TestResolve_ProtectedRawPath(t *testing.T) {
	_, err := Resolve(hostCfg(config.HostConfig{Read: []string{"~/.ssh/id_rsa"}}), "claude")
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("expected protected-path error, got %v", err)
	}
}

func TestResolve_RawGrantsMergeNoBypass(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{
		Read:     []string{"~/.myproj"},
		Allow:    []string{"~/work"},
		AllowEnv: []string{"FOO"},
	}, "claude"))
	fs := m["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["read"], []any{"~/.myproj"}) {
		t.Errorf("read = %v", fs["read"])
	}
	if !reflect.DeepEqual(fs["allow"], []any{"~/work"}) {
		t.Errorf("allow = %v", fs["allow"])
	}
	if fs["bypass_protection"] != nil {
		t.Errorf("raw grants must not add bypass_protection; got %v", fs["bypass_protection"])
	}
}

func hostCfg(h config.HostConfig) *config.Config {
	cfg := &config.Config{}
	cfg.Sandbox.Host = h
	return cfg
}
