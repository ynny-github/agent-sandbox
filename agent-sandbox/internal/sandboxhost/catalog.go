package sandboxhost

import (
	"os"
	"runtime"
	"slices"
)

// hostHome is the home directory a deny path's leading "~" expands against. A
// variable rather than a direct lookup only so tests can pin it; nothing
// outside this package sets it. Empty when the home directory cannot be
// resolved, which expand reports rather than emitting a rule that would quietly
// match nothing.
var hostHome = func() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}()

// hostOS is the platform the catalog resolves perOSAllow against. It is a
// variable rather than a direct runtime.GOOS read only so tests can assert both
// branches from one machine; nothing outside this package sets it.
//
// Resolving here — instead of emitting nono's `when` predicates — is safe
// because the profile is always generated on the host that will run under it.
// `when` exists for hand-written portable profiles and would only add a second
// dialect to a file agent-sandbox writes fresh at every launch.
var hostOS = runtime.GOOS

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
	allow []string
	// perOSAllow is read+write for one platform only, keyed by GOOS. A cache
	// whose location differs between darwin and linux belongs here rather than
	// in allow, where granting both would name a directory that does not exist
	// on the machine generating the profile.
	perOSAllow map[string][]string
	read       []string
	readFile   []string
	bypass     []string
	allowFile  []string
	allowVars  []string
	domains    []string
	// denyRead and denyEdit are paths Claude's own file tools are refused. They
	// constrain nothing else — not a brokered command, not a subprocess.
	//
	// Paths, not finished rule strings, because the rule syntax has a trap:
	// Claude Code reads the path as a gitignore pattern where a single leading
	// slash anchors at the *settings source*, not the filesystem root. A rule
	// written "Read(/etc/bashrc)" therefore matches nothing at all, silently.
	// Only "//" is absolute. Rendering the prefix in one place means no bundle
	// can get it wrong, and "~" expands here rather than being left for a
	// matcher that may or may not do it.
	denyRead []string
	denyEdit []string
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
	// Every nono runtime group is read-only, so on its own none of these four
	// can build anything: the toolchain fails on its own cache before it reaches
	// a package. Each bundle therefore adds the directories its tool writes to
	// during an ordinary build, and leaves the read surface — which layouts of
	// which version managers exist — to the group, where it is curated for us.
	//
	// None of them grants the directory that puts an executable on the host's
	// PATH (~/go/bin, ~/.cargo/bin, uv's tools dir). Installing a binary the
	// host will later run is a deliberate act; it stays a raw allow.
	"go": {
		groups: []string{"go_runtime"},
		allow:  []string{"~/go/pkg/mod"},
		// GOCACHE comes from os.UserCacheDir.
		perOSAllow: map[string][]string{
			"linux":  {"$XDG_CACHE_HOME/go-build"},
			"darwin": {"$HOME/Library/Caches/go-build"},
		},
		domains: []string{"proxy.golang.org", "sum.golang.org"},
	},
	"python": {
		groups: []string{"python_runtime"},
		// uv keeps its cache under XDG on both platforms and its managed
		// interpreters under ~/.local/share/uv; without the latter a project
		// whose Python is not installed yet cannot be provisioned.
		allow: []string{"$XDG_CACHE_HOME/uv", "~/.local/share/uv/python"},
		// pip, unlike uv, follows the platform cache directory.
		perOSAllow: map[string][]string{
			"linux":  {"$XDG_CACHE_HOME/pip"},
			"darwin": {"$HOME/Library/Caches/pip"},
		},
		// pypi.org resolves the package; files.pythonhosted.org serves the
		// sdists and wheels themselves. The index alone cannot install.
		domains: []string{"pypi.org", "files.pythonhosted.org"},
	},
	"node": {
		groups: []string{"node_runtime"},
		// ~/.npm is npm's cache on both platforms; pnpm's store follows XDG on
		// linux and lives under ~/Library on darwin. Both are read-only in the
		// group already, which is what makes an install fail rather than warn.
		allow: []string{"~/.npm", "~/.local/share/pnpm"},
		perOSAllow: map[string][]string{
			"darwin": {"~/Library/pnpm"},
		},
		domains: []string{"registry.npmjs.org"},
	},
	"rust": {
		groups: []string{"rust_runtime"},
		// The two caches cargo writes on a build; ~/.cargo itself stays
		// read-only so ~/.cargo/bin does not become writable with them.
		allow: []string{"~/.cargo/registry", "~/.cargo/git"},
		// rust_runtime grants all of ~/.cargo, which is where cargo keeps the
		// crates.io publish token. Only Claude's own file tools are blocked
		// from it: a profile-level denial is not available, because Landlock
		// has no deny-overlap on Linux and nono refuses to start when a deny
		// sits under a granted parent. Keeping the token away from brokered
		// commands means declaring "rust" under [sandbox.agent], the lever
		// docker and ssh already use.
		denyRead: []string{"~/.cargo/credentials.toml", "~/.cargo/credentials"},
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
		//
		// The tarball tree is where mise unpacks a tool before pointing
		// installs/<tool>/<version> at it by symlink. Landlock resolves that
		// symlink, so a mise-managed SDK is only usable if it is writable —
		// flutter populates its own bin/cache on first run. It rides here
		// rather than on the mise capability because it is a write grant over
		// every mise-installed binary, agent-sandbox's own included: only the
		// projects that need it should open it, and `mise` alone must keep
		// handing out a read-only tree.
		allow:     []string{"~/.config/flutter", "~/.local/share/mise/http-tarballs"},
		allowFile: []string{"~/.flutter", "~/.flutter_tool_state"},
		allowVars: []string{"FLUTTER_ROOT", "FLUTTER_STORAGE_BASE_URL"},
		// Engine artifacts and the bundled Dart SDK come from the same Google
		// Cloud Storage bucket the pub archives do.
		domains: []string{"storage.googleapis.com"},
		// A git checkout of the SDK is not covered: no fixed path is right for
		// everyone, so wherever it lives is a raw allow in the config.
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
		denyRead: []string{"~/.docker/**"},
	},
	"ssh": {
		read:      []string{"~/.ssh"},
		bypass:    []string{"~/.ssh"},
		allowFile: []string{"~/.ssh/known_hosts"},
		denyRead: []string{"~/.ssh/**"},
		denyEdit: []string{"~/.ssh/known_hosts"},
	},
	"mise": {
		read:      []string{"~/.local/share/mise", "~/.config/mise"},
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
		denyRead: []string{
			"~/.bashrc",
			"/etc/bashrc",
			"/etc/bash.bashrc",
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

// baselineGroups are nono groups every profile includes, capability list empty
// or not.
//
// nix_runtime is here rather than in the catalog because on a NixOS host it is
// not a toolchain, it is the precondition for running anything: every
// executable lives under /nix/store, reached through the /run/current-system/sw
// symlink farm, and there is no /bin/ls to fall back on. nono's base profile
// grants that tree read but not execute, so without this a brokered command
// cannot start at all — nono resolves the binary, execve is refused, and the
// command exits 127 with no output to explain itself. That is not a failure an
// operator should have to diagnose and patch per project.
//
// On a host without Nix the group's paths simply do not exist, the same way
// python_runtime names a ~/.pyenv most machines lack.
var baselineGroups = []string{"nix_runtime"}

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
