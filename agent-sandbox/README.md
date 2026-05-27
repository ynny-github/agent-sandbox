# agent-sandbox

An MCP (Model Context Protocol) server that routes shell commands to either the host machine or a Docker Compose container, based on operator-configured allow patterns.

## Install

```bash
go install github.com/ynagai/mcp-command-router@latest
```

## Configuration

Copy `config.example.toml` to `config.toml` and edit:

```toml
[server]
output_dir = "/tmp/mcp-output"
container_target = "app"

[allow_patterns]
patterns = [
  "git *",
  "make *",
]
```

## Usage

Start the project sandbox from your project root:

```bash
agent-sandbox sandbox-up -d --config agent-sandbox.toml
```

Start the MCP server:

```bash
agent-sandbox command-router --config agent-sandbox.toml
```

Stop the current project sandbox:

```bash
agent-sandbox sandbox-down --config agent-sandbox.toml
```

Remove all Docker containers and networks that appear to be managed by agent-sandbox:

```bash
agent-sandbox sandbox-prune
```

`sandbox-prune` is destructive. It removes every container labeled `cr.managed=true` and every Docker network whose name starts with `cr-sandbox-`.

Register as an MCP tool in your Claude Code settings.

## How It Works

- Commands matching an allow pattern are executed on the **host** (after shell-safety validation)
- All other commands are routed to the configured **Docker Compose service**
- Output is always written to separate stdout/stderr files; the MCP response returns file paths and exit code only
