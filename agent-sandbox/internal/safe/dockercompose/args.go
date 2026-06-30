// Package dockercompose validates and runs docker compose invocations safely.
package dockercompose

import "strings"

// ParsedArgs is the result of separating docker compose global flags from the
// subcommand and its arguments.
type ParsedArgs struct {
	GlobalFlags []string // global flags (and their values) before the subcommand
	Subcommand  string   // e.g. "up", "run"; "" when none is present
	Rest        []string // subcommand and everything after it
}

// globalValueFlags lists docker compose global flags that take a separate value
// token (the "--flag value" form). The "--flag=value" form attaches its value
// and is handled separately.
var globalValueFlags = map[string]bool{
	"-f": true, "--file": true,
	"-p": true, "--project-name": true,
	"--profile":           true,
	"--project-directory": true,
	"--env-file":          true,
	"--ansi":              true,
	"--progress":          true,
	"--parallel":          true,
}

// ParseArgs walks the leading flags, treating the first non-flag token as the
// subcommand. A known value-taking flag in "--flag value" form consumes the
// following token as its value.
func ParseArgs(args []string) ParsedArgs {
	var global []string
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		global = append(global, a)
		if globalValueFlags[a] && i+1 < len(args) {
			global = append(global, args[i+1])
			i += 2
			continue
		}
		i++
	}
	p := ParsedArgs{GlobalFlags: global}
	if i < len(args) {
		p.Subcommand = args[i]
		p.Rest = args[i:]
	}
	return p
}
