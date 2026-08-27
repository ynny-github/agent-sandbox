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
	cfg.Sandbox.Shared = h
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
// sandbox sections and asserts the generated profile, modulo the one
// intentional delta (no gh).
func TestResolve_Parity(t *testing.T) {
	r := resolve(t, config.HostConfig{
		Capabilities: []string{"go", "python", "docker", "ssh", "mise"},
	}, "claude")

	want := map[string]any{
		"extends": "claude",
		"meta":    map[string]any{"name": "custom claude"},
		"groups":  map[string]any{"include": []any{"go_runtime", "python_runtime"}},
		"filesystem": map[string]any{
			"read": []any{
				"~/.config/mise", "~/.docker", "~/.local/share/mise",
				"~/.orbstack", "~/.ssh",
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
	cfg.Sandbox.Shared = h
	return cfg
}

func TestResolveShell_ProfileShape(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"go", "mise"}
	cfg.Sandbox.Agent.Capabilities = []string{"docker", "ssh"}
	cfg.Sandbox.Shell.AllowDomains = []string{"proxy.golang.org"}
	cfg.Sandbox.Shell.AllowEnv = []string{"AWS_PROFILE"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
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

	// Nothing declared under [sandbox.agent.host] may appear here. That section
	// is the only thing keeping host credentials away from brokered commands —
	// there is no implicit exclusion behind it.
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

// The three sections and what each profile sees: the shared base reaches both,
// and each side's own section reaches only that side. This is the whole model —
// scope is decided by placement, and no grant is subtracted anywhere.
func TestSectionScoping(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Read = []string{"/srv/shared"}
	cfg.Sandbox.Agent.Read = []string{"/srv/agent-only"}
	cfg.Sandbox.Shell.Read = []string{"/srv/command-only"}

	agent, err := Resolve(cfg, "claude")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	command, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}

	agentRead := toStrings(profileMap(t, agent)["filesystem"].(map[string]any)["read"])
	commandRead := toStrings(profileMap(t, command)["filesystem"].(map[string]any)["read"])

	for _, tc := range []struct {
		path      string
		wantAgent bool
		wantCmd   bool
		section   string
	}{
		{"/srv/shared", true, true, "[sandbox.host]"},
		{"/srv/agent-only", true, false, "[sandbox.agent.host]"},
		{"/srv/command-only", false, true, "[sandbox.command.host]"},
	} {
		if got := slices.Contains(agentRead, tc.path); got != tc.wantAgent {
			t.Errorf("%s: agent profile read contains %q = %v, want %v (read = %v)", tc.section, tc.path, got, tc.wantAgent, agentRead)
		}
		if got := slices.Contains(commandRead, tc.path); got != tc.wantCmd {
			t.Errorf("%s: command profile read contains %q = %v, want %v (read = %v)", tc.section, tc.path, got, tc.wantCmd, commandRead)
		}
	}
}

// docker/ssh carry no built-in exclusion any more: declared for the command
// side they are granted there, and ProtectedGrants reports it so callers can
// warn. Without this, removing credentialCapabilities could regress into a
// silent re-exclusion and nobody would notice.
func TestResolveShell_CredentialCapabilityIsGrantedWhenDeclared(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shell.Capabilities = []string{"ssh"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	read := toStrings(profileMap(t, r)["filesystem"].(map[string]any)["read"])
	if !slices.Contains(read, "~/.ssh") {
		t.Errorf("read = %v, want to contain ~/.ssh (declared under [sandbox.command.host])", read)
	}
	if got := r.ProtectedGrants(); !slices.Equal(got, []string{"~/.ssh", "~/.ssh/known_hosts"}) {
		t.Errorf("ProtectedGrants() = %v, want [~/.ssh ~/.ssh/known_hosts]", got)
	}
}

func TestProtectedGrants_EmptyWithoutCredentialCapability(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"go", "mise"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	if got := r.ProtectedGrants(); len(got) != 0 {
		t.Errorf("ProtectedGrants() = %v, want none", got)
	}
}

// The protected-path guard on raw grants applies to the command profile too.
// It used to be unreachable there only because raw grants never reached it.
func TestResolveShell_ProtectedRawPath(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shell.Read = []string{"~/.aws"}

	if _, err := ResolveShell(cfg, "/work/project"); err == nil {
		t.Fatal("ResolveShell() error = nil, want a protected-path rejection")
	}
}

// A capability's bypass_protection and allow_file entries follow it to
// whichever side declares it. The command profile used to receive the bundle's
// read_file without its bypass, leaving the grant half-applied.
func TestResolveShell_CapabilityBypassAndAllowFile(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"bashrc"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	fs := profileMap(t, r)["filesystem"].(map[string]any)
	if got := toStrings(fs["bypass_protection"]); !slices.Contains(got, "~/.bashrc") {
		t.Errorf("bypass_protection = %v, want to contain ~/.bashrc", got)
	}
}

// Deny rules constrain the agent's own file tools, so they belong to the agent
// profile alone; emitting them for a command would be meaningless.
// A capability's domains reach the shell profile, unioned with what
// [sandbox.shell] wrote by hand: declaring "go" is enough to get the module
// proxy, so a config never has to restate a toolchain's own network needs.
func TestResolveShell_CapabilityDomains(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"go"}
	cfg.Sandbox.Shell.AllowDomains = []string{"sum.golang.org", "example.test"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	network := profileMap(t, r)["network"].(map[string]any)
	want := []string{"example.test", "proxy.golang.org", "sum.golang.org"}
	if got := toStrings(network["allow_domain"]); !slices.Equal(got, want) {
		t.Errorf("allow_domain = %v, want %v", got, want)
	}
}

// The catalog's domains are part of what a capability means, so they are
// pinned here: a bundle silently losing its registry would only show up as a
// sandboxed build that cannot fetch.
func TestResolveShell_CatalogDomains(t *testing.T) {
	for _, tc := range []struct {
		capability string
		want       []string
	}{
		{"go", []string{"proxy.golang.org", "sum.golang.org"}},
		{"python", []string{"files.pythonhosted.org", "pypi.org"}},
		{"node", []string{"registry.npmjs.org"}},
		{"rust", []string{"crates.io", "index.crates.io", "static.crates.io"}},
		{"docker", []string{
			"auth.docker.io", "index.docker.io",
			"production.cloudflare.docker.com", "registry-1.docker.io",
		}},
		{"mise", []string{"mise-versions.jdx.dev", "mise.jdx.dev"}},
		{"ssh", nil},
		{"bashrc", nil},
	} {
		t.Run(tc.capability, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Sandbox.Shell.Capabilities = []string{tc.capability}

			got, err := ShellAllowDomains(cfg)
			if err != nil {
				t.Fatalf("ShellAllowDomains() error = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s domains = %v, want %v", tc.capability, got, tc.want)
			}
		})
	}
}

// The agent profile has no network section of its own — it inherits the one in
// its nono base profile — so a capability's domains must not conjure one.
func TestResolve_CapabilityDomainsDoNotAddNetwork(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"go"}}, "claude"))
	if m["network"] != nil {
		t.Errorf("agent profile network = %#v, want none", m["network"])
	}
}

func TestShellAllowDomains(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"go"}
	cfg.Sandbox.Shell.AllowDomains = []string{"example.test"}
	// [sandbox.agent] feeds the agent profile only, and that profile has no
	// network: its capability's domains must not show up here.
	cfg.Sandbox.Agent.Capabilities = []string{"go"}

	got, err := ShellAllowDomains(cfg)
	if err != nil {
		t.Fatalf("ShellAllowDomains() error = %v", err)
	}
	want := []string{"example.test", "proxy.golang.org", "sum.golang.org"}
	if !slices.Equal(got, want) {
		t.Errorf("ShellAllowDomains() = %v, want %v", got, want)
	}
}

func TestShellAllowDomains_UnknownCapability(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shell.Capabilities = []string{"nope"}

	if _, err := ShellAllowDomains(cfg); err == nil {
		t.Fatal("ShellAllowDomains() error = nil, want an unknown-capability error")
	}
}

func TestResolveShell_NoDenyRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"ssh"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	if len(r.DenyRules) != 0 {
		t.Errorf("DenyRules = %v, want none on the command profile", r.DenyRules)
	}
}

// CommandFilesystemGrants answers "what does a sandboxed command reach outside
// its working directory" — the question agent-facing docs need. It must draw
// from the shared base and the command section only, split by write access, and
// leave out the baseline files every command gets regardless of config.
func TestShellFilesystemGrants(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"mise"}
	cfg.Sandbox.Shared.Read = []string{"/srv/shared"}
	cfg.Sandbox.Agent.Read = []string{"/srv/agent-only"}
	cfg.Sandbox.Agent.Capabilities = []string{"ssh"}
	cfg.Sandbox.Shell.Allow = []string{"/srv/cache"}
	cfg.Sandbox.Shell.ReadFile = []string{"/etc/hosts"}

	got, err := ShellFilesystemGrants(cfg)
	if err != nil {
		t.Fatalf("ShellFilesystemGrants() error = %v", err)
	}
	wantWrite := []string{"/srv/cache"}
	wantRead := []string{"/etc/hosts", "/srv/shared", "~/.config/mise", "~/.local/share/mise"}
	if !slices.Equal(got.Write, wantWrite) {
		t.Errorf("Write = %v, want %v", got.Write, wantWrite)
	}
	if !slices.Equal(got.Read, wantRead) {
		t.Errorf("Read = %v, want %v", got.Read, wantRead)
	}
}

func TestShellFilesystemGrants_EmptyConfig(t *testing.T) {
	got, err := ShellFilesystemGrants(&config.Config{})
	if err != nil {
		t.Fatalf("ShellFilesystemGrants() error = %v", err)
	}
	if len(got.Write) != 0 || len(got.Read) != 0 {
		t.Errorf("ShellFilesystemGrants() = %+v, want both lists empty (the baseline /dev/null is not a config grant)", got)
	}
}

func TestShellFilesystemGrants_UnknownCapability(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shell.Capabilities = []string{"nope"}

	if _, err := ShellFilesystemGrants(cfg); err == nil {
		t.Fatal("ShellFilesystemGrants() error = nil, want an unknown-capability error")
	}
}
