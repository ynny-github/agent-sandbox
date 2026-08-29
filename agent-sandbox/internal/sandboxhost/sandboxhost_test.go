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
	withOS(t, "linux")
	r := resolve(t, config.HostConfig{
		Capabilities: []string{"go", "python", "docker", "ssh", "mise"},
	}, "claude")

	want := map[string]any{
		"extends": "claude",
		"meta":    map[string]any{"name": "custom claude"},
		"groups":  map[string]any{"include": []any{"go_runtime", "nix_runtime", "python_runtime"}},
		"filesystem": map[string]any{
			"allow": []any{
				"$XDG_CACHE_HOME/go-build", "$XDG_CACHE_HOME/pip",
				"$XDG_CACHE_HOME/uv", "~/.local/share/uv/python", "~/go/pkg/mod",
			},
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

// Claude Code reads a permission rule's path as gitignore-style, where a single
// leading slash anchors at the settings source rather than the filesystem root
// — so "Read(/etc/bashrc)" silently matches nothing, and only the "//" prefix
// is absolute. The catalog names paths and the rendering adds the prefix, so no
// bundle can get that wrong on its own.
func TestResolve_DenyRulesFromCapabilities(t *testing.T) {
	withHome(t, "/home/test")
	r := resolve(t, config.HostConfig{Capabilities: []string{"ssh", "docker"}}, "claude")
	want := []string{
		"Edit(//home/test/.ssh/known_hosts)",
		"Read(//home/test/.docker/**)", "Read(//home/test/.ssh/**)",
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
	if m["extends"] != "claude" {
		t.Errorf("expected extends=claude; got %#v", m)
	}
	// nix_runtime is baseline, so "no capabilities" is not "no groups".
	groups := toStrings(m["groups"].(map[string]any)["include"])
	if !slices.Equal(groups, []string{"nix_runtime"}) {
		t.Errorf("baseline groups = %v, want [nix_runtime]", groups)
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
	if !reflect.DeepEqual(groups, []any{"nix_runtime", "node_runtime", "rust_runtime"}) {
		t.Errorf("groups = %v, want [nix_runtime node_runtime rust_runtime]", groups)
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
	withHome(t, "/home/test")
	r := resolve(t, config.HostConfig{Capabilities: []string{"bashrc"}}, "claude")

	fs := profileMap(t, r)["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["read_file"], []any{"/etc/bash.bashrc", "/etc/bashrc", "~/.bashrc"}) {
		t.Errorf("read_file = %v", fs["read_file"])
	}
	if !reflect.DeepEqual(fs["bypass_protection"], []any{"~/.bashrc"}) {
		t.Errorf("bypass_protection = %v", fs["bypass_protection"])
	}

	want := []string{
		"Read(//etc/bash.bashrc)", "Read(//etc/bashrc)", "Read(//home/test/.bashrc)",
	}
	if !reflect.DeepEqual(r.DenyRules, want) {
		t.Errorf("DenyRules = %v\nwant %v", r.DenyRules, want)
	}
}

// A capability can grant a read+write directory of its own. The runtime
// bundles lean on nono's *_runtime groups for that, but nono has no group for
// dart or flutter, so those bundles have to say it in the catalog.
func TestResolve_CapabilityGrantsReadWriteDirectory(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"dart"}}, "claude"))
	fs := m["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["allow"], []any{"~/.dart", "~/.pub-cache"}) {
		t.Errorf("allow = %v, want [~/.dart ~/.pub-cache]", fs["allow"])
	}
	env := toStrings(m["environment"].(map[string]any)["allow_vars"])
	for _, want := range []string{"PUB_CACHE", "PUB_HOSTED_URL"} {
		if !slices.Contains(env, want) {
			t.Errorf("allow_vars = %v, want to contain %s", env, want)
		}
	}
}

// The dart capability stops short of ~/.dart-tool, which is where pub writes
// pub-tokens.json — the credentials for private hosted repositories. Public
// dependency resolution never needs it, so it belongs with docker and ssh
// among the grants a config has to ask for deliberately, not in the bundle
// every Dart project declares.
func TestResolve_DartWithholdsPubTokens(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"dart"}}, "claude"))
	fs := m["filesystem"].(map[string]any)
	for _, list := range []string{"allow", "read", "allow_file", "read_file"} {
		for _, p := range toStrings(fs[list]) {
			if strings.HasPrefix(p, "~/.dart-tool") {
				t.Errorf("%s grants %q; pub credentials must not ride along with the bundle", list, p)
			}
		}
	}
}

// flutter carries only the Flutter-specific delta: a project declares
// ["dart", "flutter"] because the tool is a Dart tool, and duplicating the pub
// grants here would be two places to drift.
func TestResolve_FlutterCapability(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"flutter"}}, "claude"))
	fs := m["filesystem"].(map[string]any)
	if !reflect.DeepEqual(fs["allow"], []any{"~/.config/flutter", "~/.local/share/mise/http-tarballs"}) {
		t.Errorf("allow = %v", fs["allow"])
	}
	if !reflect.DeepEqual(fs["allow_file"], []any{"/dev/null", "~/.flutter", "~/.flutter_tool_state"}) {
		t.Errorf("allow_file = %v", fs["allow_file"])
	}
	if got := toStrings(fs["allow"]); slices.Contains(got, "~/.pub-cache") {
		t.Errorf("allow = %v, want the pub cache to come from the dart capability", got)
	}
	env := toStrings(m["environment"].(map[string]any)["allow_vars"])
	if !slices.Contains(env, "FLUTTER_ROOT") {
		t.Errorf("allow_vars = %v, want to contain FLUTTER_ROOT", env)
	}
}

// mise unpacks a tool under http-tarballs and points installs/<tool>/<version>
// at it by symlink. Landlock resolves that symlink, so a mise-managed Flutter
// SDK is only writable — which flutter requires, it populates its own bin/cache
// — if that tree is granted.
//
// It rides on flutter rather than on mise because it is a write grant over
// every mise-installed binary, agent-sandbox's own included. Only the projects
// that need it should open it, and mise on its own must keep handing out a
// read-only tree.
func TestResolve_FlutterGrantsMiseToolPayloadsWritable(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"flutter"}}, "claude"))
	if got := toStrings(m["filesystem"].(map[string]any)["allow"]); !slices.Contains(got, "~/.local/share/mise/http-tarballs") {
		t.Errorf("flutter allow = %v, want to contain ~/.local/share/mise/http-tarballs", got)
	}
}

func TestResolve_MiseAloneStaysReadOnly(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"mise"}}, "claude"))
	fs := m["filesystem"].(map[string]any)
	if got := toStrings(fs["allow"]); len(got) != 0 {
		t.Errorf("mise allow = %v, want no writable grant", got)
	}
	if got := toStrings(fs["read"]); !slices.Contains(got, "~/.local/share/mise") {
		t.Errorf("read = %v, want the mise tree read-only", got)
	}
}

// Every nono runtime group is read-only, so a bundle that leans on one still
// cannot build: the go command fails on GOCACHE before it reaches a package,
// and uv, npm and cargo fail the same way on theirs. Where a toolchain writes
// during an ordinary build is part of what the bundle means.
func TestResolve_RuntimeCapabilitiesGrantWritableCaches(t *testing.T) {
	withOS(t, "linux")
	for _, tc := range []struct {
		capability string
		want       []string
	}{
		{"go", []string{"$XDG_CACHE_HOME/go-build", "~/go/pkg/mod"}},
		{"python", []string{"$XDG_CACHE_HOME/pip", "$XDG_CACHE_HOME/uv", "~/.local/share/uv/python"}},
		{"node", []string{"~/.local/share/pnpm", "~/.npm"}},
		{"rust", []string{"~/.cargo/git", "~/.cargo/registry"}},
	} {
		t.Run(tc.capability, func(t *testing.T) {
			m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{tc.capability}}, "claude"))
			if got := toStrings(m["filesystem"].(map[string]any)["allow"]); !slices.Equal(got, tc.want) {
				t.Errorf("allow = %v, want %v", got, tc.want)
			}
		})
	}
}

// The read surface still comes from the group — the bundle adds writes, it does
// not restate what nono already curates.
func TestResolve_RuntimeCapabilitiesLeaveReadToTheGroup(t *testing.T) {
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"go"}}, "claude"))
	if got := toStrings(m["filesystem"].(map[string]any)["read"]); len(got) != 0 {
		t.Errorf("read = %v, want ~/go to keep coming from go_runtime", got)
	}
}

// A cache whose location differs by platform is resolved when the profile is
// generated, because that always happens on the machine that will run under it.
// Go picks GOCACHE from os.UserCacheDir, which is ~/Library/Caches on darwin
// and $XDG_CACHE_HOME on linux.
func TestResolve_PlatformScopedGrants(t *testing.T) {
	withOS(t, "darwin")
	m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{"go"}}, "claude"))
	got := toStrings(m["filesystem"].(map[string]any)["allow"])
	if !slices.Contains(got, "$HOME/Library/Caches/go-build") {
		t.Errorf("allow = %v, want the darwin build cache", got)
	}
	if slices.Contains(got, "$XDG_CACHE_HOME/go-build") {
		t.Errorf("allow = %v, want the linux build cache left out on darwin", got)
	}
}

// The write stops short of the directories that put an executable on the host's
// PATH — `go install`, `cargo install`, `uv tool install`. Those stay a
// deliberate raw allow rather than something a project gets for declaring the
// bundle.
func TestResolve_RuntimeCapabilitiesWithholdInstallTargets(t *testing.T) {
	withOS(t, "linux")
	for capability, unwanted := range map[string][]string{
		"go":     {"~/go", "~/go/bin"},
		"rust":   {"~/.cargo", "~/.cargo/bin", "~/.rustup"},
		"python": {"~/.local/share/uv/tools", "~/.local/bin"},
	} {
		t.Run(capability, func(t *testing.T) {
			m := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{capability}}, "claude"))
			got := toStrings(m["filesystem"].(map[string]any)["allow"])
			for _, p := range unwanted {
				if slices.Contains(got, p) {
					t.Errorf("allow = %v, want %s withheld", got, p)
				}
			}
		})
	}
}

// A capability must never put a denial inside a directory its own group grants.
// Landlock has no deny-overlap on Linux, so nono refuses to start at all:
//
//	Sandbox initialization failed: Landlock deny-overlap is not enforceable
//	deny '~/.cargo/credentials' overlaps allowed parent '~/.cargo'
//	  (source: group:rust_runtime)
//
// That is every brokered command failing, not a narrower grant. Narrowing a
// group by path is simply not available; the profile-level lever is which
// section declares the capability, and nothing finer.
func TestResolve_NoProfileLevelDenials(t *testing.T) {
	for _, name := range CapabilityNames() {
		t.Run(name, func(t *testing.T) {
			agent := profileMap(t, resolve(t, config.HostConfig{Capabilities: []string{name}}, "claude"))
			if got := agent["filesystem"].(map[string]any)["deny"]; got != nil {
				t.Errorf("agent filesystem.deny = %v, want none", got)
			}

			cfg := &config.Config{}
			cfg.Sandbox.Shared.Capabilities = []string{name}
			r, err := ResolveShell(cfg, "/work/project")
			if err != nil {
				t.Fatalf("ResolveShell() error = %v", err)
			}
			if got := profileMap(t, r)["filesystem"].(map[string]any)["deny"]; got != nil {
				t.Errorf("shell filesystem.deny = %v, want none", got)
			}
		})
	}
}

// Claude's own file tools are blocked from it regardless, the way docker and
// ssh block theirs. This is the agent side's only protection: it constrains
// Read/Edit, not a command.
func TestResolve_RustDeniesCargoCredentialsToClaudeTools(t *testing.T) {
	withHome(t, "/home/test")
	r := resolve(t, config.HostConfig{Capabilities: []string{"rust"}}, "claude")
	want := []string{
		"Read(//home/test/.cargo/credentials)",
		"Read(//home/test/.cargo/credentials.toml)",
	}
	if !slices.Equal(r.DenyRules, want) {
		t.Errorf("DenyRules = %v, want %v", r.DenyRules, want)
	}
}

// A brokered command declared with "rust" can read the publish token, because
// rust_runtime grants ~/.cargo whole and nothing can carve the file back out.
// Keeping it away from commands means declaring the capability under
// [sandbox.agent], the same lever docker and ssh use.
func TestResolveShell_RustGrantsCargoWithoutCarveOut(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Shared.Capabilities = []string{"rust"}

	r, err := ResolveShell(cfg, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	m := profileMap(t, r)
	if allow := toStrings(m["filesystem"].(map[string]any)["allow"]); !slices.Contains(allow, "~/.cargo/registry") {
		t.Errorf("allow = %v, want the registry cache granted", allow)
	}
	if len(r.DenyRules) != 0 {
		t.Errorf("DenyRules = %v, want none on the shell profile", r.DenyRules)
	}
}

// withHome pins the home directory deny paths expand against, so the expected
// rules can be written out rather than rebuilt from the same call the code uses.
func withHome(t *testing.T, home string) {
	t.Helper()
	prev := hostHome
	hostHome = home
	t.Cleanup(func() { hostHome = prev })
}

// withOS pins the platform the catalog resolves against, so a test can assert
// both branches of a grant that differs by OS from whichever machine runs it.
func withOS(t *testing.T, goos string) {
	t.Helper()
	prev := hostOS
	hostOS = goos
	t.Cleanup(func() { hostOS = prev })
}

// nix_runtime is baseline rather than a capability. On a NixOS host every
// executable lives under /nix/store, reached through the /run/current-system/sw
// symlink farm, and nono's base profile grants that tree read but not execute —
// so without it no brokered command starts at all, and the failure is a silent
// exit 127. That is not something an operator should have to discover and patch
// per project. On a machine without Nix the group's paths simply do not exist,
// the same way python_runtime names a ~/.pyenv most hosts lack.
func TestBaselineGroups_ReachBothProfiles(t *testing.T) {
	agent := profileMap(t, resolve(t, config.HostConfig{}, "claude"))
	if got := toStrings(agent["groups"].(map[string]any)["include"]); !slices.Contains(got, "nix_runtime") {
		t.Errorf("agent groups = %v, want to contain nix_runtime", got)
	}

	r, err := ResolveShell(&config.Config{}, "/work/project")
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	shell := profileMap(t, r)
	if got := toStrings(shell["groups"].(map[string]any)["include"]); !slices.Contains(got, "nix_runtime") {
		t.Errorf("shell groups = %v, want to contain nix_runtime", got)
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
		// pub.dev resolves the package; the archives themselves are served
		// from Google Cloud Storage, which is also where flutter fetches its
		// engine artifacts and the Dart SDK it bundles.
		{"dart", []string{"pub.dev", "storage.googleapis.com"}},
		{"flutter", []string{"storage.googleapis.com"}},
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

func TestCapabilityNames_MatchesCatalog(t *testing.T) {
	got := CapabilityNames()
	if len(got) != len(catalog) {
		t.Fatalf("CapabilityNames() returned %d names, catalog has %d: %v", len(got), len(catalog), got)
	}
	for _, name := range got {
		if _, ok := catalog[name]; !ok {
			t.Errorf("CapabilityNames() returned %q, which is not in the catalog", name)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("CapabilityNames() is not sorted: %v", got)
	}
}
