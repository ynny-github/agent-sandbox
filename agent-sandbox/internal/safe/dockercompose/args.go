// Package dockercompose validates and runs docker compose invocations safely.
package dockercompose

import "strings"

// ParsedArgs is the result of separating docker compose global flags from the
// subcommand and its arguments.
type ParsedArgs struct {
	GlobalFlags  []string // global flags (and their values) before the subcommand
	Subcommand   string   // e.g. "up", "run"; "" when none is present
	Rest         []string // subcommand and everything after it
	Unrecognized string   // a leading flag we could not classify; "" when none
}

// globalValueFlags lists docker compose global flags that take a value. In the
// "--flag value" form the value is a separate token; the "--flag=value" and
// short "-fvalue" forms attach it.
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

// globalBoolFlags lists docker compose global flags that take no value.
var globalBoolFlags = map[string]bool{
	"--all-resources": true,
	"--compatibility": true,
	"--dry-run":       true,
}

// ParseArgs walks the leading flags, treating the first non-flag token as the
// subcommand. Every leading flag must be a recognized docker compose global
// flag; an unrecognized one sets Unrecognized and stops parsing, so the caller
// can fail closed rather than misidentify the subcommand and run it anyway.
func ParseArgs(args []string) ParsedArgs {
	var global []string
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}

		// "--flag=value" / "-f=value": the key is the part before "=".
		key := a
		attached := false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			key = a[:eq]
			attached = true
		}
		// Short value flag with an attached value, e.g. "-fcompose.yml".
		if !attached && len(a) > 2 && a[1] != '-' && globalValueFlags[a[:2]] {
			global = append(global, a)
			i++
			continue
		}

		switch {
		case globalValueFlags[key]:
			global = append(global, a)
			if !attached && i+1 < len(args) {
				global = append(global, args[i+1])
				i += 2
				continue
			}
			i++
		case globalBoolFlags[key]:
			global = append(global, a)
			i++
		default:
			return ParsedArgs{GlobalFlags: global, Unrecognized: a}
		}
	}

	p := ParsedArgs{GlobalFlags: global}
	if i < len(args) {
		p.Subcommand = args[i]
		p.Rest = args[i:]
	}
	return p
}
