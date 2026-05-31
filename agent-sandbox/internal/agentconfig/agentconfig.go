package agentconfig

const snippet = `## Command Router

When native shell commands are unavailable, use the ` + "`run_command`" + ` MCP tool instead.
Allowed commands run on the host; all others run in an isolated container.
Output is written to files; read the returned paths for stdout/stderr.
`

func Content() string {
	return snippet
}
