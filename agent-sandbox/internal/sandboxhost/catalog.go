package sandboxhost

// capability is a named bundle that expands into nono grants (groups, reads,
// bypass exemptions, allow-files, env vars) plus the Claude permission-deny
// rules for any credential path it exposes.
type capability struct {
	groups    []string
	read      []string
	readFile  []string
	bypass    []string
	allowFile []string
	allowVars []string
	deny      []string
}

// catalog is the fixed, built-in set of capabilities. Unknown names are errors.
//
// The credential-exposing bundles ("docker", "ssh") carry no special handling:
// like every other capability they apply to whichever side declares them, so
// keeping host keys away from brokered commands is a matter of declaring them
// under [sandbox.agent] rather than the shared [sandbox.shared].
var catalog = map[string]capability{
	"go":     {groups: []string{"go_runtime"}},
	"python": {groups: []string{"python_runtime"}},
	"node":   {groups: []string{"node_runtime"}},
	"rust":   {groups: []string{"rust_runtime"}},
	"docker": {
		read:   []string{"~/.docker", "~/.orbstack"},
		bypass: []string{"~/.docker"},
		deny:   []string{"Read(~/.docker/**)"},
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
		read:      []string{"~/.local/share/mise", "~/.config/mise"},
		allowVars: []string{"MISE*", "__MISE*"},
	},
	"taskgate": {read: []string{"~/.local/state/taskgate"}},
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
