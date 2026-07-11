// Package gh implements the "safe gh" wrapper: it parses a gh argv and reports
// invocations outside a narrow allowlist so the command layer can refuse them.
package gh

import "strings"

// Invocation is a gh command line split into any options that appear before the
// command, the command (primary resource, e.g. "pr"), the subcommand (verb,
// e.g. "list"), and the verb's remaining arguments.
type Invocation struct {
	Global     []string
	Command    string
	Subcommand string
	Args       []string
}

// Parse splits argv into leading options, the command, the subcommand, and the
// subcommand's args. Leading options are kept (fail closed) so an unknown option
// cannot be mistaken for the command; the subcommand is the first non-option
// token after the command, so an option wedged before the verb cannot hide it.
func Parse(argv []string) Invocation {
	var inv Invocation
	i := 0
	for i < len(argv) && strings.HasPrefix(argv[i], "-") {
		inv.Global = append(inv.Global, argv[i])
		i++
	}
	if i >= len(argv) {
		return inv
	}
	inv.Command = argv[i]
	i++
	for i < len(argv) {
		if !strings.HasPrefix(argv[i], "-") {
			inv.Subcommand = argv[i]
			inv.Args = argv[i+1:]
			return inv
		}
		i++
	}
	return inv
}
