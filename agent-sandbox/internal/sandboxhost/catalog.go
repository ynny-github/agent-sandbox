package sandboxhost

// capability is a named bundle that expands into nono grants (groups, reads,
// bypass exemptions, allow-files, env vars) plus the Claude permission-deny
// rules for any credential path it exposes.
type capability struct {
	groups    []string
	read      []string
	bypass    []string
	allowFile []string
	allowVars []string
	deny      []string
}

// catalog is the fixed, built-in set of capabilities. Unknown names are errors.
var catalog = map[string]capability{
	"go":     {groups: []string{"go_runtime"}},
	"python": {groups: []string{"python_runtime"}},
	"docker": {
		read:   []string{"~/.docker", "~/.orbstack"},
		bypass: []string{"~/.docker"},
		deny:   []string{"Read(~/.docker/**)", "Glob(~/.docker/**)", "Grep(~/.docker/**)"},
	},
	"ssh": {
		read:      []string{"~/.ssh"},
		bypass:    []string{"~/.ssh"},
		allowFile: []string{"~/.ssh/known_hosts"},
		deny: []string{
			"Read(~/.ssh/**)", "Glob(~/.ssh/**)", "Grep(~/.ssh/**)",
			"Write(~/.ssh/known_hosts)", "Update(~/.ssh/known_hosts)",
		},
	},
	"mise": {
		read:      []string{"~/.local/share/mise"},
		allowVars: []string{"MISE*", "__MISE*"},
	},
	"taskgate": {read: []string{"~/.local/state/taskgate"}},
}

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
var baselineEnv = []string{"PATH", "HOME", "TERM", "LANG", "LC_ALL", "USER"}
var baselineAllowFile = []string{"/dev/null"}

// protectedPrefixes are paths nono denies by default. A raw read/allow grant
// targeting one is rejected (raw grants never add bypass_protection); the user
// must go through a capability instead.
var protectedPrefixes = []string{
	"~/.ssh", "~/.aws", "~/.docker", "~/.gnupg", "~/.config/gh", "~/.kube",
}
