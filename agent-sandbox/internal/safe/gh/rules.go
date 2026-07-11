package gh

import (
	"fmt"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

// allowedVerbs maps each in-scope command to the set of verbs permitted for it.
// Anything not listed — including destructive and future verbs — is refused.
var allowedVerbs = map[string]map[string]bool{
	"pr": set(
		"list", "view", "diff", "checks", "status",
		"create", "edit", "comment", "reopen", "ready", "review", "lock", "unlock",
	),
	"issue": set(
		"list", "view", "status",
		"create", "edit", "comment", "reopen", "pin", "unpin", "lock", "unlock",
	),
	"project": set(
		"list", "view", "item-list", "field-list",
		"create", "edit", "copy", "item-add", "item-create", "item-edit",
		"item-archive", "field-create", "link", "unlink", "mark-template",
	),
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Check parses argv and returns a safe.Violation when the invocation is refused,
// or nil when it is allowed. Refusal is fail-closed: only in-scope commands and
// their explicitly allowlisted verbs pass. Help-only invocations are allowed.
func Check(argv []string) []safe.Violation {
	inv := Parse(argv)
	if isHelp(inv) {
		return nil
	}
	if msg := refuse(inv); msg != "" {
		return []safe.Violation{{Source: "cli", Setting: msg}}
	}
	return nil
}

// refuse returns a reason string when the invocation is outside the allowlist,
// or "" when it is permitted.
func refuse(inv Invocation) string {
	if inv.Command == "api" {
		return "gh api is not permitted in the safe scope"
	}
	verbs, ok := allowedVerbs[inv.Command]
	if !ok {
		return fmt.Sprintf(
			"gh %s is outside the safe scope; only issue, pr, and project are permitted",
			inv.Command)
	}
	if !verbs[inv.Subcommand] {
		return fmt.Sprintf(
			"gh %s %s is not permitted (only read and non-destructive operations are allowed)",
			inv.Command, inv.Subcommand)
	}
	return ""
}

// isHelp reports whether the invocation only prints help and has no side effect:
// a bare command with no verb, or any invocation carrying --help/-h.
func isHelp(inv Invocation) bool {
	if inv.Command == "" || inv.Subcommand == "" {
		return true
	}
	return hasHelpFlag(inv.Global) || hasHelpFlag(inv.Args)
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
