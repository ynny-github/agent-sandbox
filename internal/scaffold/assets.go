// Package scaffold downloads a starting set of files from this repository's
// main branch and places them in a project. It never overwrites, and it never
// transforms what it fetches: what is on main is what lands on disk.
package scaffold

import "os"

// BaseURL is where every asset is fetched from. It is pinned to main on
// purpose, not to the binary's own version: init then works the moment a
// template lands on main, and the profiles are validated against the
// operator's own nono before anything is written.
const BaseURL = "https://raw.githubusercontent.com/ynny-github/agent-sandbox/main/"

// Asset is one file init places.
type Asset struct {
	// Source is the path under the repository root, appended to BaseURL.
	Source string
	// Dest is the path relative to the project directory.
	Dest string
	// Profile marks a nono profile. Profiles are handed to
	// `nono profile validate` before any file is written.
	Profile bool
	// Mode is the file mode Write creates the file with; 0 means 0o644.
	Mode os.FileMode
}

// Assets is the fixed set init places. The skills are seeded once and never
// updated: the project owns them from that point on, which is why there is no
// --force and no refresh path.
var Assets = []Asset{
	{Source: "templates/minimal/.agent-sandbox/bin/claude", Dest: ".agent-sandbox/bin/claude", Mode: 0o755},
	{Source: "templates/minimal/.agent-sandbox/bash", Dest: ".agent-sandbox/bash", Mode: 0o755},
	{Source: "templates/minimal/.agent-sandbox/settings.json", Dest: ".agent-sandbox/settings.json"},
	{Source: "templates/minimal/.agent-sandbox/command-profile.json", Dest: ".agent-sandbox/command-profile.json", Profile: true},
	{Source: "templates/minimal/.agent-sandbox/claude-profile.json", Dest: ".agent-sandbox/claude-profile.json", Profile: true},
	{
		Source: ".claude/skills/growing-a-nono-profile/SKILL.md",
		Dest:   ".claude/skills/growing-a-nono-profile/SKILL.md",
	},
	{
		Source: ".claude/skills/verifying-nono-sandbox-claims/SKILL.md",
		Dest:   ".claude/skills/verifying-nono-sandbox-claims/SKILL.md",
	},
}
