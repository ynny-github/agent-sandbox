package git

import (
	"strings"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

// Rule is one entry in the semantic denylist. Match reports whether the parsed
// invocation is refused; Message is the human-readable reason surfaced to the
// user; ID is a stable key for tests and diagnostics.
type Rule struct {
	ID      string
	Message string
	Match   func(inv Invocation) bool
}

// Rules returns the semantic denylist, a port of the agent-sandbox.toml git
// drop patterns with two deliberate differences: --force-with-lease is allowed,
// and -c hook/signature bypasses are detected.
func Rules() []Rule { return rules }

// Check parses argv and returns a safe.Violation for every matched rule,
// in Rules() order. A nil/empty result means the invocation is allowed.
// (internal/safe/git imports internal/safe; safe does not import git, so there
// is no package cycle.)
func Check(argv []string) []safe.Violation {
	inv := Parse(argv)
	var out []safe.Violation
	for _, r := range rules {
		if r.Match(inv) {
			out = append(out, safe.Violation{Source: "cli", Setting: r.Message})
		}
	}
	return out
}

// isGitFalse reports whether v is one of git's boolean-false spellings.
func isGitFalse(v string) bool {
	switch strings.ToLower(v) {
	case "", "false", "0", "no", "off":
		return true
	}
	return false
}

// execCapableConfigKeys are git config keys whose value git executes as a
// command; injecting one via -c/--config-env is a code-execution vector.
// (core.hooksPath is intentionally omitted — it is covered by bypass-hooks.)
var execCapableConfigKeys = map[string]bool{
	"core.sshcommand": true,
	"core.pager":      true,
	"core.fsmonitor":  true,
	"core.editor":     true,
	"sequence.editor": true,
	"diff.external":   true,
}

var rules = []Rule{
	{
		ID:      "force-push",
		Message: "git push --force is not allowed; use --force-with-lease instead",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "push" {
				return false
			}
			// Deletion / mirror / prune are destructive regardless of any lease flag.
			if hasLong(inv.Args, "--delete") || hasShort(inv.Args, 'd') {
				return true
			}
			if hasLong(inv.Args, "--mirror") || hasLong(inv.Args, "--prune") {
				return true
			}
			for _, a := range inv.Args {
				// A leading ":" deletes a ref; a leading "+" force-updates it. Both
				// are destructive refspecs that need no --force flag.
				if strings.HasPrefix(a, ":") || strings.HasPrefix(a, "+") {
					return true
				}
			}
			// Unconditional --force/-f is always blocked, even when a lease flag is
			// also present: --force-with-lease / --force-if-includes do not neutralize
			// a co-present --force, so git would still force-push unconditionally.
			if hasLong(inv.Args, "--force") || hasShort(inv.Args, 'f') {
				return true
			}
			// --force-with-lease / --force-if-includes without an unconditional
			// --force is the sanctioned safe form.
			return false
		},
	},
	{
		ID:      "hard-reset",
		Message: "git reset --hard is not allowed",
		Match:   func(inv Invocation) bool { return inv.Subcommand == "reset" && hasLong(inv.Args, "--hard") },
	},
	{
		ID:      "clean-force",
		Message: "git clean -f is not allowed",
		Match: func(inv Invocation) bool {
			return inv.Subcommand == "clean" && (hasLong(inv.Args, "--force") || hasShort(inv.Args, 'f'))
		},
	},
	{
		ID:      "branch-force-delete",
		Message: "force-deleting a branch is not allowed",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "branch" {
				return false
			}
			if hasShort(inv.Args, 'D') {
				return true
			}
			delete := hasLong(inv.Args, "--delete") || hasShort(inv.Args, 'd')
			force := hasLong(inv.Args, "--force") || hasShort(inv.Args, 'f')
			return delete && force
		},
	},
	{
		ID:      "filter-history",
		Message: "rewriting history with filter-branch/filter-repo is not allowed",
		Match: func(inv Invocation) bool {
			return inv.Subcommand == "filter-branch" || inv.Subcommand == "filter-repo"
		},
	},
	{
		ID:      "update-ref-delete",
		Message: "git update-ref -d is not allowed",
		Match: func(inv Invocation) bool {
			return inv.Subcommand == "update-ref" && (hasLong(inv.Args, "--delete") || hasShort(inv.Args, 'd'))
		},
	},
	{
		ID:      "reflog-expire",
		Message: "git reflog expire is not allowed",
		Match: func(inv Invocation) bool {
			return inv.Subcommand == "reflog" && subAction(inv.Args) == "expire"
		},
	},
	{
		ID:      "gc-prune",
		Message: "git gc --prune=now is not allowed",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "gc" {
				return false
			}
			for _, a := range inv.Args {
				if a == "--prune=now" || a == "--prune=all" {
					return true
				}
			}
			return false
		},
	},
	{
		ID:      "bypass-hooks",
		Message: "bypassing git hooks or signatures is not allowed",
		Match: func(inv Invocation) bool {
			if hasLong(inv.Args, "--no-verify") || hasLong(inv.Args, "--no-gpg-sign") {
				return true
			}
			if inv.Subcommand == "commit" && hasShort(inv.Args, 'n') { // -n is --no-verify only for commit
				return true
			}
			for _, g := range inv.Global {
				// -c <k>=<v> and --config-env=<k>=<envvar> both set config that can
				// disable hooks or signing; git honors them identically.
				if g.Name != "-c" && g.Name != "--config-env" {
					continue
				}
				k, v, ok := strings.Cut(g.Value, "=")
				if !ok {
					continue
				}
				switch strings.ToLower(k) {
				case "core.hookspath":
					return true
				case "commit.gpgsign":
					// For --config-env the value is an env var name we cannot resolve,
					// so treat any env-sourced gpgsign override as suspect.
					if g.Name == "--config-env" || isGitFalse(v) {
						return true
					}
				}
			}
			return false
		},
	},
	{
		ID:      "alias-injection",
		Message: "injecting a git alias via -c/--config-env is not allowed",
		Match: func(inv Invocation) bool {
			for _, g := range inv.Global {
				if g.Name != "-c" && g.Name != "--config-env" {
					continue
				}
				if k, _, ok := strings.Cut(g.Value, "="); ok &&
					strings.HasPrefix(strings.ToLower(k), "alias.") {
					return true
				}
			}
			return false
		},
	},
	{
		ID:      "config-exec-injection",
		Message: "injecting an exec-capable git config key via -c/--config-env is not allowed",
		Match: func(inv Invocation) bool {
			for _, g := range inv.Global {
				if g.Name != "-c" && g.Name != "--config-env" {
					continue
				}
				if k, _, ok := strings.Cut(g.Value, "="); ok && execCapableConfigKeys[strings.ToLower(k)] {
					return true
				}
			}
			return false
		},
	},
	{
		ID:      "stash-destroy",
		Message: "git stash drop/clear is not allowed",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "stash" {
				return false
			}
			a := subAction(inv.Args)
			return a == "drop" || a == "clear"
		},
	},
	{
		ID:      "remote-tamper",
		Message: "changing git remotes is not allowed",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "remote" {
				return false
			}
			a := subAction(inv.Args)
			return a == "remove" || a == "rm" || a == "set-url"
		},
	},
	{
		ID:      "tag-delete",
		Message: "deleting a git tag is not allowed",
		Match: func(inv Invocation) bool {
			return inv.Subcommand == "tag" && (hasLong(inv.Args, "--delete") || hasShort(inv.Args, 'd'))
		},
	},
	{
		ID:      "discard-changes",
		Message: "discarding working-tree changes is not allowed",
		Match: func(inv Invocation) bool {
			switch inv.Subcommand {
			case "checkout":
				for _, a := range inv.Args {
					if a == "--" || a == "." {
						return true
					}
				}
				return false
			case "restore":
				// restore affects the working tree unless it targets only the index.
				if subAction(inv.Args) == "" && !hasLong(inv.Args, "--source") {
					return false // no path given
				}
				if hasLong(inv.Args, "--worktree") || hasShort(inv.Args, 'W') {
					return true
				}
				return !hasLong(inv.Args, "--staged") // default target is the worktree
			}
			return false
		},
	},
	{
		ID:      "config-write",
		Message: "writing git config is not allowed (reads only)",
		Match: func(inv Invocation) bool {
			if inv.Subcommand != "config" {
				return false
			}
			readOnly := []string{"--get", "--get-all", "--get-regexp", "--get-urlmatch",
				"--get-color", "--get-colorbool", "--list"}
			for _, ro := range readOnly {
				if hasLong(inv.Args, ro) {
					return false
				}
			}
			if hasShort(inv.Args, 'l') {
				return false
			}
			return true
		},
	},
}
