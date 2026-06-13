package router

import (
	"regexp"
	"strings"
)

func Route(cmd string, allowPatterns, denyPatterns []string) string {
	for _, pattern := range denyPatterns {
		if matchPattern(pattern, cmd) {
			return "container"
		}
	}
	for _, pattern := range allowPatterns {
		if matchPattern(pattern, cmd) {
			return "host"
		}
	}
	return "container"
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
