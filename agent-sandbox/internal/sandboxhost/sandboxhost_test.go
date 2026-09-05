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
	cfg.Sandbox.Agent = h
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
		"groups":  map[string]any{"include": []any{"git_config", "go_runtime", "nix_runtime", "python_runtime"}},
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
	// The baseline groups mean "no capabilities" is not "no groups".
	groups := toStrings(m["groups"].(map[string]any)["include"])
	if !slices.Equal(groups, []string{"git_config", "nix_runtime"}) {
		t.Errorf("baseline groups = %v, want [git_config nix_runtime]", groups)
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
	if !reflect.DeepEqual(groups, []any{"git_config", "nix_runtime", "node_runtime", "rust_runtime"}) {
		t.Errorf("groups = %v, want [git_config nix_runtime node_runtime rust_runtime]", groups)
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
	if !reflect.DeepEqual(fs["allow"], []any{"~/.dart", "~/.dart-tool", "~/.pub-cache"}) {
		t.Errorf("allow = %v, want [~/.dart ~/.dart-tool ~/.pub-cache]", fs["allow"])
	}
	env := toStrings(m["environment"].(map[string]any)["allow_vars"])
	for _, want := range []string{"PUB_CACHE", "PUB_HOSTED_URL"} {
		if !slices.Contains(env, want) {
			t.Errorf("allow_vars = %v, want to contain %s", env, want)
		}
	}
}

// ~/.dart-tool holds pub-tokens.json, the credentials for private hosted
// repositories, and it is also where dartdev's analytics writes its config on
// startup — so withholding it does not protect the token, it stops dart from
// running at all:
//
//	PathAccessException: Creation failed, path = '/home/yn/.dart-tool'
//
// The directory is granted and the token is denied to Claude's own file tools,
// the arrangement rust ended up with for the same reason: a profile-level
// carve-out inside a granted directory is not enforceable on Linux.
func TestResolve_DartGrantsDartToolAndDeniesTheTokenToClaude(t *testing.T) {
	withHome(t, "/home/test")
	r := resolve(t, config.HostConfig{Capabilities: []string{"dart"}}, "claude")
	if got := toStrings(profileMap(t, r)["filesystem"].(map[string]any)["allow"]); !slices.Contains(got, "~/.dart-tool") {
		t.Errorf("allow = %v, want to contain ~/.dart-tool", got)
	}
	want := []string{"Read(//home/test/.dart-tool/pub-tokens.json)"}
	if !slices.Equal(r.DenyRules, want) {
		t.Errorf("DenyRules = %v, want %v", r.DenyRules, want)
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
	// The pre-XDG ~/.flutter and ~/.flutter_tool_state are deliberately absent.
	// They are bare files in $HOME, and allow_file grants their contents, not
	// the right to unlink or recreate them — every one of those needs write on
	// the parent directory, which here is $HOME. flutter deletes and rewrites
	// its state file, so granting the file is worse than not granting it: the
	// file's mere existence is what makes flutter prefer the legacy path over
	// ~/.config/flutter, where it is a directory and all three work.
	if !reflect.DeepEqual(fs["allow_file"], []any{"/dev/null"}) {
		t.Errorf("allow_file = %v, want the baseline only", fs["allow_file"])
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
// git_config is the other baseline group. Toolchains shell out to git without
// saying so — flutter's launcher reads the SDK revision that way and exits 128
// without it, go build stamps a version, npm and cargo resolve git
// dependencies — so requiring each bundle to remember it would mean finding out
// the same way each time. The group is configuration only; ~/.git-credentials
// is not in it, so nothing here hands out a credential.
func TestBaselineGroups_ReachTheProfile(t *testing.T) {
	agent := profileMap(t, resolve(t, config.HostConfig{}, "claude"))
	for _, want := range []string{"nix_runtime", "git_config"} {
		if got := toStrings(agent["groups"].(map[string]any)["include"]); !slices.Contains(got, want) {
			t.Errorf("agent groups = %v, want to contain %s", got, want)
		}
	}
}

func hostCfg(h config.HostConfig) *config.Config {
	cfg := &config.Config{}
	cfg.Sandbox.Agent = h
	return cfg
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

// docker/ssh carry no built-in exclusion: declaring one under [sandbox.agent]
// grants it there, and ProtectedGrants reports it so a caller can warn. Without
// this, removing that reporting could regress into a silent re-exclusion and
// nobody would notice.
func TestResolve_CredentialCapabilityIsGrantedWhenDeclared(t *testing.T) {
	r := resolve(t, config.HostConfig{Capabilities: []string{"ssh"}}, "claude")
	read := toStrings(profileMap(t, r)["filesystem"].(map[string]any)["read"])
	if !slices.Contains(read, "~/.ssh") {
		t.Errorf("read = %v, want to contain ~/.ssh (declared under [sandbox.agent])", read)
	}
	if got := r.ProtectedGrants(); !slices.Equal(got, []string{"~/.ssh", "~/.ssh/known_hosts"}) {
		t.Errorf("ProtectedGrants() = %v, want [~/.ssh ~/.ssh/known_hosts]", got)
	}
}

func TestProtectedGrants_EmptyWithoutCredentialCapability(t *testing.T) {
	r := resolve(t, config.HostConfig{Capabilities: []string{"go", "mise"}}, "claude")
	if got := r.ProtectedGrants(); len(got) != 0 {
		t.Errorf("ProtectedGrants() = %v, want none", got)
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
