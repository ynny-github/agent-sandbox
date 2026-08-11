# agent-sandbox

Routes an AI coding agent's shell commands to either the host machine or a sandboxed `nono run` invocation, based on operator-configured allow patterns.

## Install

```bash
go install github.com/ynny-github/agent-sandbox/agent-sandbox@latest
```

## Configuration

Copy `config.example.toml` to `config.toml` and edit:

```toml
[mcp]
command_output_dir = "/tmp/mcp-output"

[sandbox.network]
allow_domains = ["proxy.golang.org"]   # extra domains on top of nono's "developer" network profile

[sandbox.command]
allow = [
  "git *",
  "make *",
]
```

## Usage

Start the MCP server:

```bash
agent-sandbox command-router --config agent-sandbox.toml
```

Check whether external dependencies are usable on this host:

```bash
agent-sandbox doctor
```

`doctor` verifies that `nono` is on `PATH` and that the command broker can
actually bind a unix socket in its socket directory
(`$XDG_STATE_HOME/agent-sandbox`, or `~/.local/state/agent-sandbox` when
`XDG_STATE_HOME` is unset) — the same way `agent-sandbox claude` does at
launch. Exits 0 when all checks pass, 1 otherwise.

Run Claude inside the nono sandbox. Options after `--` go to `claude`; the
sandbox profile is generated from `[sandbox.host]` in `agent-sandbox.toml`,
and `agent-sandbox` no longer forwards options to `nono`:

```bash
agent-sandbox claude -- --model opus
```

`agent-sandbox claude` starts a host-side command broker (a per-launch Unix
socket server) before launching Claude, and tears it down when Claude exits.
Sandboxed commands never run in a persistent container: each one is sent to
the broker, which runs it under its own `nono run` invocation, scoped to the
current working directory and the `[sandbox.network]` policy. There is
nothing to start beforehand — no `sandbox up` step. If `nono` cannot be found
or the broker socket cannot be created, Claude is not launched; run
`agent-sandbox doctor` to diagnose.

`agent-sandbox debug` accepts the same form and prints the resulting `nono`
command without running it, followed by the generated nono profile JSON and the
GitHub MCP config JSON (with the token redacted) for inspection.

Register as an MCP tool in your Claude Code settings.

Route a single command through the router from the shell (streams output live):

```bash
agent-sandbox exec --config agent-sandbox.toml -- git status
```

### Tool mode

`tool_mode` in `agent-sandbox.toml` selects how the agent's commands reach the
router:

- `mcp` (default): the `claude` launcher disables the Bash and Monitor tools, and
  the agent routes commands through the `run_command` MCP tool.
- `hook`: the launcher leaves Bash and Monitor enabled and injects a PreToolUse
  hook via `claude --settings` at launch, so each command is rewritten to
  `agent-sandbox exec -- <command>` by `agent-sandbox hook`. No prior setup is
  needed and nothing is written to `.claude/settings.json`. `agent-sandbox` must
  be on `PATH`.

### Environment variables (`--env`)

`--env` is a global flag that loads variables from an env file into the
process before it launches Claude or runs a command. It is repeatable and uses a
scheme-based reference; only the `file:` source exists today:

```bash
agent-sandbox claude --env file:.env -- --model opus
agent-sandbox exec --env file:.env -- go test ./...
```

The file is a minimal dotenv subset: `KEY=VALUE` per line, `#` comments and
blank lines ignored, an optional `export ` prefix stripped, and surrounding
quotes removed. There is no variable interpolation. Values **override** any
same-named host environment variable; with multiple `--env` files, later files
win.

For `agent-sandbox claude`, the loaded keys are also added to the sandbox
profile's allowed env vars, so the sandboxed Claude process — and, in hook mode,
the commands it runs through `agent-sandbox exec` — can read them.

### GitHub MCP

The built-in GitHub MCP server is enabled when the `GITHUB_MCP_TOKEN`
environment variable is non-empty; otherwise it is not configured. Supply it via
`--env` or the ambient environment. Its value is passed to the MCP server as
`GITHUB_PERSONAL_ACCESS_TOKEN`.

```bash
agent-sandbox claude --env file:.secrets.env -- --model opus
# where .secrets.env contains: GITHUB_MCP_TOKEN=ghp_...
```

### `[sandbox.host]`

`sandbox.host` in `agent-sandbox.toml` controls the host-side access granted
to the sandboxed agent; it is translated into the nono profile generated at
launch. `capabilities` are named bundles — `go`, `python`, `node`, `rust`,
`docker`, `ssh`, `mise`, `taskgate`, `bashrc` — each expanding to the
directories, files, and env vars that capability needs. Raw grants (`allow`,
`read`, `allow_file`, `read_file`, `allow_env`) cover anything not already
covered by a capability. The common
`PATH`/`HOME`/... env vars and `/dev/null` are always granted from a built-in
baseline.

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

- Commands matching an allow pattern are executed on the **host** (after shell-safety validation).
- Commands matching a drop pattern are **refused** — neither the host nor the sandbox runs them; the response carries exit code 1 and a stderr line. Each drop entry is a `{ pattern, message }` table; when `message` is set it is printed on refusal, otherwise the default `dropped: command matches drop pattern "<pattern>"` line is used.
- All other commands are sent to the host-side command broker started by `agent-sandbox claude`, which runs each one under its own **`nono run`** invocation, scoped to the current working directory and nono's `developer` network profile plus `sandbox.network.allow_domains`.
- Allow wins over drop: a command matching both an allow and a drop pattern runs on the host.
- Output is always written to separate stdout/stderr files; the MCP response returns file paths and exit code only.

`sandbox.command.drop` example:

```toml
[sandbox.command]
drop = [
  { pattern = "git *" },
  { pattern = "gh *", message = "gh is disabled in this sandbox. Use the GitHub MCP server's tools instead." },
]
```

## Migrating from an older config

The configuration was reorganized; old keys are no longer accepted.

| Old | New |
|---|---|
| `server.output_dir` | `mcp.command_output_dir` |
| `sandbox.build_context` | removed — commands run under nono, not Docker |
| `sandbox.dockerfile` | removed — commands run under nono, not Docker |
| `sandbox.image` | removed — commands run under nono, not Docker |
| `sandbox.external_network` | removed — commands run under nono, not Docker |
| `sandbox.container` (whole section) | removed — commands run under nono, not Docker |
| `sandbox.network.allow_external` | removed — use `sandbox.network.allow_domains` |
| `sandbox.allow_cidrs` | removed — network access is now `sandbox.network.allow_domains` |
| `sandbox.allow_hosts` | removed — network access is now `sandbox.network.allow_domains` |
| `sandbox.network.allow_cidrs` | removed — replaced by `sandbox.network.allow_domains` |
| `sandbox.network.allow_hosts` | removed — replaced by `sandbox.network.allow_domains` |
| `[allow_patterns] patterns` | `sandbox.command.allow` |
| `[drop_patterns] patterns` | `sandbox.command.drop` |
| `[deny_patterns] patterns` | removed — move destructive entries into `sandbox.command.drop` |
| `[container] env_passthrough` | `sandbox.command.env_passthrough` |
| `sandbox.container.env_passthrough` | `sandbox.command.env_passthrough` |
| `[nono] profile` | removed — configure the sandbox profile in `[sandbox.host]` (nono options are no longer forwarded) |
| `agent-sandbox sandbox up/down/prune` | removed — `agent-sandbox claude` starts and stops the per-launch command broker automatically |

The `deny` routing axis is gone. Patterns that previously forced a host-allowed command into the sandbox now have two options: leave them out of `allow` (so they default to the sandbox), or add them to `drop` if they should be refused entirely.
