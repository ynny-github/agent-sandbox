// Package git implements the "safe git" wrapper: it parses a git argv and
// reports known-dangerous invocations so the command layer can refuse them.
package git

import "strings"

// GlobalOpt is a git global option appearing before the subcommand, e.g.
// {Name: "-c", Value: "core.hooksPath=/dev/null"} or {Name: "-p"}.
type GlobalOpt struct {
	Name  string
	Value string
}

// Invocation is a git command line split into its global options, the
// subcommand, and the subcommand's own arguments (flags and positionals).
type Invocation struct {
	Global     []GlobalOpt
	Subcommand string
	Args       []string
}

// globalValueOpts are git global options that consume a value, whether
// attached (--git-dir=X, -C<path>, -c<k=v>) or separate (--git-dir X, -c k=v).
var globalValueOpts = map[string]bool{
	"-C": true, "-c": true,
	"--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--config-env": true,
}

// globalBoolOpts are git global options that take no value.
var globalBoolOpts = map[string]bool{
	"-p": true, "--paginate": true, "--no-pager": true, "--bare": true,
	"--no-replace-objects": true, "--literal-pathspecs": true,
	"--glob-pathspecs": true, "--icase-pathspecs": true,
	"--no-optional-locks": true,
}

// Parse splits argv into global options followed by the first non-global
// token (the subcommand) and its remaining args.
func Parse(argv []string) Invocation {
	var inv Invocation
	i := 0
	for i < len(argv) {
		name, val, hasVal := splitOpt(argv[i])
		switch {
		case globalBoolOpts[name] && !hasVal:
			inv.Global = append(inv.Global, GlobalOpt{Name: name})
			i++
		case globalValueOpts[name]:
			if hasVal {
				inv.Global = append(inv.Global, GlobalOpt{Name: name, Value: val})
				i++
			} else if i+1 < len(argv) {
				inv.Global = append(inv.Global, GlobalOpt{Name: name, Value: argv[i+1]})
				i += 2
			} else {
				inv.Global = append(inv.Global, GlobalOpt{Name: name})
				i++
			}
		default:
			// A token that is not a recognized global option: if it does not look
			// like an option it is the subcommand, and the rest are its args. If it
			// DOES start with "-", it is an UNRECOGNIZED global option — record it and
			// keep scanning, so an unknown global (e.g. "--no-advice") cannot hide the
			// real subcommand from the denylist. Fail closed, not open.
			if !strings.HasPrefix(argv[i], "-") {
				inv.Subcommand = argv[i]
				inv.Args = argv[i+1:]
				return inv
			}
			inv.Global = append(inv.Global, GlobalOpt{Name: name, Value: val})
			i++
		}
	}
	return inv
}

// splitOpt breaks a token into an option name and an attached value.
// It handles "--name=value", bare "--name"/"-x", and short value options
// with an attached value ("-C/path", "-ckey=val").
func splitOpt(tok string) (name, val string, hasVal bool) {
	if strings.HasPrefix(tok, "--") {
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			return tok[:eq], tok[eq+1:], true
		}
		return tok, "", false
	}
	if strings.HasPrefix(tok, "-") && len(tok) > 2 {
		if short := tok[:2]; globalValueOpts[short] {
			return short, tok[2:], true
		}
	}
	return tok, "", false
}

// hasLong reports whether args contains the exact long flag or its "flag=value" form.
func hasLong(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// hasShort reports whether ch appears in any short-flag cluster (e.g. 'f' in "-fd").
func hasShort(args []string, ch byte) bool {
	for _, a := range args {
		if len(a) >= 2 && a[0] == '-' && a[1] != '-' {
			for i := 1; i < len(a); i++ {
				if a[i] == ch {
					return true
				}
			}
		}
	}
	return false
}

// subAction returns the first non-flag token in args (the second-level
// action for e.g. "stash drop", "remote remove"), or "" if none.
func subAction(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}
