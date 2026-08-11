package sandboxhost

import (
	"encoding/json"
	"reflect"
	"slices"
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
				"~/.config/mise", "~/.docker", "~/.local/share/mise",
				"~/.local/state/taskgate", "~/.orbstack", "~/.ssh",
			},
			"allow_file":        []any{"/dev/null", "~/.ssh/known_hosts"},
			"bypass_protection": []any{"~/.docker", "~/.ssh"},
		},
		"environment": map[string]any{
			"allow_vars": []any{"AGENT_SANDBOX_BROKER_SOCKET", "HOME", "LANG", "LC_ALL", "MISE*", "PATH", "TERM", "USER", "__MISE*"},
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
		"Read(~/.docker/**)", "Read(~/.ssh/**)",
	}
	if !reflect.DeepEqual(r.DenyRules, want) {
		t.Errorf("DenyRules = %v\nwant %v", r.DenyRules, want)
	}
}

func TestResolve_BaselineOnlyWhenEmpty(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{}, "claude"))
	env := m["environment"].(map[string]any)["allow_vars"].([]any)
	if !reflect.DeepEqual(env, []any{"AGENT_SANDBOX_BROKER_SOCKET", "HOME", "LANG", "LC_ALL", "PATH", "TERM", "USER"}) {
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
	_, err := Resolve(hostCfg(config.HostConfig{Capabilities: []string{"java"}}), "claude")
	if err == nil || !strings.Contains(err.Error(), "java") {
		t.Fatalf("expected unknown-capability error naming java, got %v", err)
	}
}

func TestResolve_RuntimeGroups(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{
		Capabilities: []string{"node", "rust"},
	}, "claude"))
	groups := m["groups"].(map[string]any)["include"].([]any)
	if !reflect.DeepEqual(groups, []any{"node_runtime", "rust_runtime"}) {
		t.Errorf("groups = %v, want [node_runtime rust_runtime]", groups)
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

func TestResolve_BashrcCapability(t *testing.T) {
	r := resolve(t, config.HostConfig{Capabilities: []string{"bashrc"}}, "claude")

	fs := profileMap(t, r)["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["read_file"], []any{"/etc/bash.bashrc", "/etc/bashrc", "~/.bashrc"}) {
		t.Errorf("read_file = %v", fs["read_file"])
	}
	if !reflect.DeepEqual(fs["bypass_protection"], []any{"~/.bashrc"}) {
		t.Errorf("bypass_protection = %v", fs["bypass_protection"])
	}

	want := []string{
		"Read(/etc/bash.bashrc)", "Read(/etc/bashrc)", "Read(~/.bashrc)",
	}
	if !reflect.DeepEqual(r.DenyRules, want) {
		t.Errorf("DenyRules = %v\nwant %v", r.DenyRules, want)
	}
}

func hostCfg(h config.HostConfig) *config.Config {
	cfg := &config.Config{}
	cfg.Sandbox.Host = h
	return cfg
}

func TestResolveCommand_ProfileShape(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Host.Capabilities = []string{"go", "docker", "ssh", "mise"}
	cfg.Sandbox.Network.AllowDomains = []string{"proxy.golang.org"}
	cfg.Sandbox.Command.EnvPassthrough = []string{"AWS_PROFILE"}

	r, err := ResolveCommand(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveCommand() error = %v", err)
	}
	data, err := r.ProfileJSON()
	if err != nil {
		t.Fatalf("ProfileJSON() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}

	network, ok := got["network"].(map[string]any)
	if !ok {
		t.Fatalf("profile has no network section: %s", data)
	}
	if network["network_profile"] != "developer" {
		t.Errorf("network_profile = %v, want developer", network["network_profile"])
	}
	if !slices.Contains(toStrings(network["allow_domain"]), "proxy.golang.org") {
		t.Errorf("allow_domain = %v, want to contain proxy.golang.org", network["allow_domain"])
	}

	fs := got["filesystem"].(map[string]any)
	if !slices.Contains(toStrings(fs["allow"]), "/work/project") {
		t.Errorf("filesystem.allow = %v, want to contain /work/project", fs["allow"])
	}

	// Credential capabilities must NOT leak into the command profile.
	all := string(data)
	for _, forbidden := range []string{".ssh", ".docker", ".orbstack"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("command profile must not grant %s: %s", forbidden, all)
		}
	}

	env := got["environment"].(map[string]any)
	if !slices.Contains(toStrings(env["allow_vars"]), "AWS_PROFILE") {
		t.Errorf("allow_vars = %v, want to contain AWS_PROFILE", env["allow_vars"])
	}

	// The broker socket path must never be allow-listed for a brokered
	// command's own sandbox: a command that could reach the broker could
	// recurse into it (broker.Server.Serve caps no concurrency), turning a
	// self-dial into a host-side fork bomb outside the sandbox.
	if slices.Contains(toStrings(env["allow_vars"]), "AGENT_SANDBOX_BROKER_SOCKET") {
		t.Errorf("allow_vars = %v, must not contain AGENT_SANDBOX_BROKER_SOCKET", env["allow_vars"])
	}
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}
	return out
}
