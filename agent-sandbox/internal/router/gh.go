package router

// ghDisabledMessage is printed when a gh invocation is dropped. gh is disabled
// in the sandbox so the agent uses the GitHub MCP server's tools instead of the
// gh CLI, whose argv surface is impractical to allowlist safely.
const ghDisabledMessage = "gh is disabled in this sandbox. Use the GitHub MCP server's tools instead."

// isGhInvocation reports whether seg invokes the gh CLI — its first token is
// exactly "gh". Prefixes such as "ghi" or "github" do not match.
func isGhInvocation(seg Segment) bool {
	return len(seg.Args) > 0 && seg.Args[0] == "gh"
}
