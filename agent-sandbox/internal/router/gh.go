package router

import "regexp"

// ghDisabledMessage is printed when a gh invocation is dropped. gh is disabled
// in the sandbox so the agent uses the GitHub MCP server's tools instead of the
// gh CLI, whose argv surface is impractical to allowlist safely.
const ghDisabledMessage = "gh is disabled in this sandbox. Use the GitHub MCP server's tools instead."

// isGhInvocation reports whether seg invokes the gh CLI — its first token is
// exactly "gh". Prefixes such as "ghi" or "github" do not match.
func isGhInvocation(seg Segment) bool {
	return len(seg.Args) > 0 && seg.Args[0] == "gh"
}

// ghFallbackRe matches a "gh" token in a shell command position: at the start
// of the line or after whitespace or a command delimiter (; | & ( or a
// backtick), and followed by whitespace or end of line. "github"/"ghi" (no
// following boundary) and "longhorn"/"high" (no leading boundary) do not match.
var ghFallbackRe = regexp.MustCompile("(^|[\\s;|&(`])gh(\\s|$)")

// containsGhCommand reports whether raw contains a gh CLI invocation in a
// command position. It backstops the fallback path (command substitution,
// backticks, background) that bypasses per-segment routing, and errs toward
// over-matching (fail closed): the sandbox refuses rather than risk letting a
// gh call through.
func containsGhCommand(raw string) bool {
	return ghFallbackRe.MatchString(raw)
}
