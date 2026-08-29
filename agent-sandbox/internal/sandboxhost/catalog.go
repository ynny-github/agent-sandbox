package sandboxhost

import "slices"

// capability is a named bundle that expands into nono grants (groups, reads,
// bypass exemptions, allow-files, env vars, network domains) plus the Claude
// permission-deny rules for any credential path it exposes.
//
// Two fields reach only one of the two profiles, because only one of them has
// somewhere to put them: deny constrains the agent's own file tools, and
// domains widen the network of the shell sandbox brokered commands run in (the
// agent's own network comes from its nono base profile, which agent-sandbox
// does not configure). Both are still declared per capability rather than per
// side, so a bundle stays one entry no matter which section names it.
type capability struct {
	groups []string
	// allow is read+write, for a bundle that has no nono group to lean on. The
	// runtime bundles get their writable caches from *_runtime groups; nono has
	// no group for dart or flutter, so those say it here instead.
	allow     []string
	read      []string
	readFile  []string
	bypass    []string
	allowFile []string
	allowVars []string
	domains   []string
	deny      []string
}

// catalog is the fixed, built-in set of capabilities. Unknown names are errors.
//
// The credential-exposing bundles ("docker", "ssh") carry no special handling:
// like every other capability they apply to whichever side declares them, so
// keeping host keys away from brokered commands is a matter of declaring them
// under [sandbox.agent] rather than the shared [sandbox.shared].
var catalog = map[string]capability{
	// Each runtime names its own registry rather than leaning on the developer
	// network profile, which covers most of them today: a preset that changes
	// under us must not silently take a toolchain's package fetches with it.
	// Listing a domain the preset already grants is harmless — the two are
	// unioned.
	"go": {
		groups:  []string{"go_runtime"},
		domains: []string{"proxy.golang.org", "sum.golang.org"},
	},
	"python": {
		groups: []string{"python_runtime"},
		// pypi.org resolves the package; files.pythonhosted.org serves the
		// sdists and wheels themselves. The index alone cannot install.
		domains: []string{"pypi.org", "files.pythonhosted.org"},
	},
	"node": {
		groups:  []string{"node_runtime"},
		domains: []string{"registry.npmjs.org"},
	},
	"rust": {
		groups: []string{"rust_runtime"},
		// index.crates.io is cargo's sparse index (the default protocol);
		// static.crates.io serves the .crate files.
		domains: []string{"crates.io", "index.crates.io", "static.crates.io"},
	},
	// dart and flutter are two bundles rather than one because flutter is a
	// Dart tool: a Flutter project declares ["dart", "flutter"], and everything
	// pub-related stays in one place instead of being restated here and drifting.
	//
	// Neither has a nono group behind it, so both spell out their writable
	// state. Deliberately absent: ~/.dart-tool, where pub keeps
	// pub-tokens.json — the credentials for private hosted repositories. Public
	// dependency resolution never touches it, so it belongs with docker and ssh
	// among the grants a config asks for on purpose.
	"dart": {
		// ~/.pub-cache holds the downloaded packages and the binaries of
		// globally activated ones; ~/.dart is dartdev's own settings file.
		allow:     []string{"~/.pub-cache", "~/.dart"},
		allowVars: []string{"PUB_CACHE", "PUB_HOSTED_URL"},
		// pub.dev resolves a package; the archives are served from Google Cloud
		// Storage, so the index alone cannot install.
		domains: []string{"pub.dev", "storage.googleapis.com"},
	},
	"flutter": {
		// ~/.config/flutter is the tool's current state directory; the two
		// files are the pre-XDG locations it still reads and rewrites.
		allow:     []string{"~/.config/flutter"},
		allowFile: []string{"~/.flutter", "~/.flutter_tool_state"},
		allowVars: []string{"FLUTTER_ROOT", "FLUTTER_STORAGE_BASE_URL"},
		// Engine artifacts and the bundled Dart SDK come from the same Google
		// Cloud Storage bucket the pub archives do.
		domains: []string{"storage.googleapis.com"},
		// Not granted here: the SDK checkout itself. flutter writes into its own
		// bin/cache, so wherever the SDK lives must be writable — under mise
		// that is the http-tarballs tree the mise capability grants, and for a
		// git checkout it is a raw allow in the config, because no fixed path
		// is right for everyone.
	},
	"docker": {
		read:   []string{"~/.docker", "~/.orbstack"},
		bypass: []string{"~/.docker"},
		// A pull needs all four: index/registry for the manifest, auth for the
		// bearer token, and the Cloudflare host for the layer blobs.
		domains: []string{
			"auth.docker.io",
			"index.docker.io",
			"production.cloudflare.docker.com",
			"registry-1.docker.io",
		},
		deny: []string{"Read(~/.docker/**)"},
	},
	"ssh": {
		read:      []string{"~/.ssh"},
		bypass:    []string{"~/.ssh"},
		allowFile: []string{"~/.ssh/known_hosts"},
		deny: []string{
			"Read(~/.ssh/**)",
			"Edit(~/.ssh/known_hosts)",
		},
	},
	"mise": {
		read: []string{"~/.local/share/mise", "~/.config/mise"},
		// mise unpacks a tool under http-tarballs and points
		// installs/<tool>/<version> at it by symlink. Landlock resolves that
		// symlink, so a toolchain that writes inside its own install root needs
		// this tree writable rather than the read-only view above — flutter
		// populating bin/cache on first run is the case that forced it. The
		// rest of the mise tree stays read-only, so `mise install` from a
		// sandboxed command is still refused by the profile.
		allow:     []string{"~/.local/share/mise/http-tarballs"},
		allowVars: []string{"MISE*", "__MISE*"},
		// mise's own hosts: self-update and the version listings. Where it
		// fetches a tool *from* is per-tool (GitHub releases, nodejs.org,
		// python-build-standalone, ...) and cannot be enumerated here, so a
		// project that runs `mise install` sandboxed adds those to
		// [sandbox.shell] allow_domains itself.
		domains: []string{"mise.jdx.dev", "mise-versions.jdx.dev"},
	},
	"bashrc": {
		readFile: []string{"~/.bashrc", "/etc/bashrc", "/etc/bash.bashrc"},
		bypass:   []string{"~/.bashrc"},
		deny: []string{
			"Read(~/.bashrc)",
			"Read(/etc/bashrc)",
			"Read(/etc/bash.bashrc)",
		},
	},
}

// CapabilityNames returns the catalog's capability names, sorted. It exists so
// the agent-facing documentation can list the names an operator (or the agent
// editing the config) may actually write, rather than restating the catalog in
// prose where it would drift the moment a bundle is added.
func CapabilityNames() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// shellNetworkProfile is the nono network profile every brokered command runs
// under. Fixed by design; not configurable.
const shellNetworkProfile = "developer"

// agentBase maps a launch agent to its nono base profile and profile name.
type agentBase struct {
	extends  string
	metaName string
}

var agentBases = map[string]agentBase{
	"claude": {extends: "claude", metaName: "custom claude"},
}

// baselineEnv / baselineAllowFile are granted for every agent regardless of the
// selected capabilities, so common shell/locale env and generic files never
// have to be repeated per project.
//
// baselineEnv is shared by Resolve (the launched agent's profile) and
// ResolveShell (the per-command shell profile). Deliberately NOT included
// here: "AGENT_SANDBOX_BROKER_SOCKET" (the literal value of
// broker.SocketEnvVar). It is added only in Resolve, via agentOnlyEnv below,
// so the per-command sandbox never allow-lists it. A brokered command that
// could reach the broker socket would let it recurse into
// broker.Server.Serve, which spawns handlers with no concurrency cap — a
// host-side nono fork bomb outside the sandbox, not a privilege escalation,
// but worth foreclosing structurally rather than relying on the socket
// happening to live outside every path the command profile grants.
var baselineEnv = []string{"PATH", "HOME", "TERM", "LANG", "LC_ALL", "USER"}
var baselineAllowFile = []string{"/dev/null"}

// agentOnlyEnv is granted only to the launched agent's own profile (Resolve),
// never to the per-command shell profile (ResolveShell). See baselineEnv's
// comment for why "AGENT_SANDBOX_BROKER_SOCKET" belongs here instead of
// there: it is the literal value of broker.SocketEnvVar, duplicated (rather
// than imported) to keep this package free of a dependency on internal/broker.
// Without it, nono would strip the variable and Claude could never reach the
// command broker.
var agentOnlyEnv = []string{"AGENT_SANDBOX_BROKER_SOCKET"}

// protectedPrefixes are paths nono denies by default. A raw read/allow grant
// targeting one is rejected (raw grants never add bypass_protection); the user
// must go through a capability instead.
var protectedPrefixes = []string{
	"~/.ssh", "~/.aws", "~/.docker", "~/.gnupg", "~/.config/gh", "~/.kube",
}
