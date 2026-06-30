# agent-sandbox

An MCP (Model Context Protocol) server that routes shell commands to either the host machine or a Docker Compose container, based on operator-configured allow patterns.

## Install

```bash
go install github.com/ynagai/mcp-command-router@latest
```

## Configuration

Copy `config.example.toml` to `config.toml` and edit:

```toml
[mcp]
command_output_dir = "/tmp/mcp-output"

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "myapp"

[sandbox.command]
allow = [
  "git *",
  "make *",
]
```

## Usage

Start the project sandbox from your project root:

```bash
agent-sandbox sandbox up -d --config agent-sandbox.toml
```

Start the MCP server:

```bash
agent-sandbox command-router --config agent-sandbox.toml
```

Stop the current project sandbox:

```bash
agent-sandbox sandbox down --config agent-sandbox.toml
```

To stop a sandbox that belongs to a different directory, pass `--path`:

```bash
agent-sandbox sandbox down --path /path/to/other/project
```

The path must match the directory the sandbox was started from. `down` only needs the project name derived from the path, so it works even if that directory's build context or config is unavailable.

Remove all Docker containers and networks that appear to be managed by agent-sandbox:

```bash
agent-sandbox sandbox prune
```

`sandbox prune` is destructive. It removes every container labeled `cr.managed=true` and every Docker network whose name starts with `cr-sandbox-`.

Check whether external dependencies are usable on this host:

```bash
agent-sandbox doctor
```

`doctor` verifies that `nono` is on `PATH`, that `docker compose version` works (which also accepts compatible CLIs like colima or podman that alias `docker`), and that the Docker daemon is reachable. Exits 0 when all checks pass, 1 otherwise.

Run Claude inside the nono sandbox. Options before `--` go to `nono wrap`
(including `--profile`); options after `--` go to `claude`:

```bash
agent-sandbox claude --profile nono.jsonc -- --model opus
```

`agent-sandbox debug` accepts the same form and prints the resulting `nono`
command without running it.

Register as an MCP tool in your Claude Code settings.

Route a single command through the router from the shell (streams output live):

```bash
agent-sandbox exec --config agent-sandbox.toml -- git status
```

Install the Claude Code PreToolUse hook so Bash and Monitor commands route
through the sandbox automatically:

```bash
agent-sandbox install-hook
```

This merges a PreToolUse hook into `.claude/settings.json` (matchers `Bash` and
`Monitor`) that rewrites each command to `agent-sandbox exec -- <command>` via
`agent-sandbox hook`. `agent-sandbox` must be on `PATH`.

### Tool mode

`tool_mode` in `agent-sandbox.toml` selects how the agent's commands reach the
router:

- `mcp` (default): the `claude` launcher disables the Bash and Monitor tools, and
  the agent routes commands through the `run_command` MCP tool.
- `hook`: the launcher leaves Bash and Monitor enabled, and the PreToolUse hook
  rewrites each command to `agent-sandbox exec -- <command>`. This requires the
  hook to be installed (`agent-sandbox install-hook`); `agent-sandbox claude`
  refuses to start in `hook` mode if the hook is missing for either the `Bash` or
  `Monitor` matcher.

## Safe wrappers

`agent-sandbox safe <tool> ...` runs a tool only after validating that its
invocation is safe, then passes the command through unchanged.

### `safe docker-compose`

```bash
agent-sandbox safe docker-compose up -d
```

This resolves the project with `docker compose config` and refuses the
invocation (exit 1, running nothing) when any of the following hold:

- a `bind` mount resolves to a path outside the current working directory;
- a `bind` mount targets the Docker socket (`docker.sock`);
- a service sets `privileged: true`, `network_mode: host`, `pid: host`,
  `ipc: host`, or `userns_mode: host`;
- a service exposes host `devices`;
- `cap_add` contains a dangerous capability (e.g. `SYS_ADMIN`, `NET_ADMIN`);
- `security_opt` disables confinement (`seccomp:unconfined`,
  `apparmor:unconfined`, `label:disable`);
- the subcommand is `run` or `exec`.

Named volumes and `tmpfs` mounts are allowed, and every other subcommand
(`up`, `build`, `down`, `ps`, `logs`, ...) passes through. The danger rules are
fixed and built-in.

## How It Works

- Commands matching a drop pattern are **refused** — neither the host nor the container runs them; the MCP response carries exit code 1 and a stderr file containing `dropped: command matches drop pattern "<pattern>"`.
- Commands matching an allow pattern are executed on the **host** (after shell-safety validation).
- All other commands are routed to the configured **Docker Compose service**.
- Drop wins over allow.
- Output is always written to separate stdout/stderr files; the MCP response returns file paths and exit code only.

## Migrating from an older config

The configuration was reorganized; old keys are no longer accepted.

| Old | New |
|---|---|
| `server.output_dir` | `mcp.command_output_dir` |
| `sandbox.build_context` | `sandbox.container.build_context` |
| `sandbox.dockerfile` | `sandbox.container.dockerfile` |
| `sandbox.image` | `sandbox.container.image` |
| `sandbox.external_network` | `sandbox.container.external_network` |
| `sandbox.allow_cidrs` | `sandbox.network.allow_cidrs` |
| `sandbox.allow_hosts` | `sandbox.network.allow_hosts` |
| `[allow_patterns] patterns` | `sandbox.command.allow` |
| `[drop_patterns] patterns` | `sandbox.command.drop` |
| `[deny_patterns] patterns` | removed — move destructive entries into `sandbox.command.drop` |
| `[container] env_passthrough` | `sandbox.container.env_passthrough` |
| `[nono] profile` | removed — pass `--profile <name>` to `agent-sandbox claude` instead |

The `deny` routing axis is gone. Patterns that previously forced a host-allowed command into the sandbox now have two options: leave them out of `allow` (so they default to the sandbox), or add them to `drop` if they should be refused entirely.
