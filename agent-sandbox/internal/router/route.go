package router

import (
	"regexp"
	"strings"
)

// DropRule is a drop pattern with an optional custom refusal message. An empty
// Message means the caller should print the default "dropped: ..." line.
type DropRule struct {
	Pattern string
	Message string
}

// Route decides where cmd runs. Allow is checked before drop, so a command
// matching both an allow and a drop pattern runs on the host. On a drop, matched
// is the pattern and message is the rule's custom message (empty for the
// default).
func Route(cmd string, allow []string, drop []DropRule) (decision, matched, message string) {
	for _, pattern := range allow {
		if matchPattern(pattern, cmd) {
			return "host", pattern, ""
		}
	}
	for _, rule := range drop {
		if matchPattern(rule.Pattern, cmd) {
			return "drop", rule.Pattern, rule.Message
		}
	}
	return "container", "", ""
}

func matchPattern(pattern, cmd string) bool {
	var b strings.Builder
	b.WriteString("(?s)^")
	for _, r := range pattern {
		if r == '*' {
			b.WriteString(".*")
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	b.WriteString("$")

	matched, _ := regexp.MatchString(b.String(), cmd)
	return matched
}
