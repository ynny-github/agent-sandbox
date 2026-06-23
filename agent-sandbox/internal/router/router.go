package router

import (
	"regexp"
	"strings"
)

func Route(cmd string, allow, deny, drop []string) (decision, matched string) {
	for _, pattern := range drop {
		if matchPattern(pattern, cmd) {
			return "drop", pattern
		}
	}
	for _, pattern := range deny {
		if matchPattern(pattern, cmd) {
			return "container", pattern
		}
	}
	for _, pattern := range allow {
		if matchPattern(pattern, cmd) {
			return "host", pattern
		}
	}
	return "container", ""
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
