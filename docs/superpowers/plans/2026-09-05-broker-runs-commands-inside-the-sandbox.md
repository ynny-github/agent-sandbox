# Broker Runs Commands Inside the Sandbox — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the command broker inside its own `nono run` session, have it interpret the agent's shell line in-process and exec each command itself, so every command is governed by an operator-written nono command profile.

**Architecture:** The launcher starts two sibling sandboxes — `nono wrap … claude` for the agent and `nono run … agent-sandbox broker` for commands. The broker is a policy-controlled command with no `exec_paths` for anything it must not reach directly. It parses the agent's command line with `mvdan.cc/sh/v3` and routes every simple command through `interp.ExecHandler`, which is the only place an `execve` happens. Commands split into a policy tier (own child sandbox plus argv rules, reached only through nono's shim) and a floor tier (named in the broker's `exec_paths`, running in the broker's own sandbox).

**Tech Stack:** Go 1.25, `mvdan.cc/sh/v3` v3.13.1, `spf13/cobra`, `BurntSushi/toml`, nono 0.74.0.

**Spec:** [docs/superpowers/specs/2026-09-05-broker-runs-commands-inside-the-sandbox.md](../specs/2026-09-05-broker-runs-commands-inside-the-sandbox.md)

Supporting evidence for every measured claim the spec makes: [docs/superpowers/specs/2026-09-05-broker-probes.md](../specs/2026-09-05-broker-probes.md)

## Global Constraints

- Output language for all deliverables — code comments, doc comments, docs, commit messages — is **English** (`.claude/CLAUDE.md`).
- Commits follow Conventional Commits per `.claude/rules/git-commit.md`: `<type>(<scope>): <subject>`, imperative subject under 72 characters, body explaining *What* and *Why* when the change is non-trivial. `lefthook` validates the title on commit.
- Go module is `github.com/ynny-github/agent-sandbox`; package source lives under `agent-sandbox/`. Tests run from the repo root with `go test ./...`.
- New dependency: `mvdan.cc/sh/v3` at `v3.13.1`. No other dependency is added. v3.14.0 declares `go 1.26.0`, which would raise this project's toolchain floor above the `go = "1.25"` pinned in `.mise.toml`; `go.mod`'s `go` directive stays `1.25.5`.
- `bin/agent-sandbox` is on PATH via `.mise.toml`, built by `mise run build`. It is **not** a valid location for the broker binary in a real deployment — nono refuses a policy command binary whose parent directory the sandbox can write. Tests must never depend on the broker binary's location.
- The command profile must never declare a shell (`bash`, `sh`) in either tier.
- Existing public behaviour that must keep working: `agent-sandbox claude`, `agent-sandbox exec`, `agent-sandbox command-router` (MCP mode), `agent-sandbox doctor`, `agent-sandbox debug`.

---

### Task 1: Resolve the command profile from config

**Files:**
- Modify: `agent-sandbox/internal/config/config.go`
- Modify: `agent-sandbox/internal/config/errors.go`
- Test: `agent-sandbox/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config.CommandProfile string` (TOML key `command_profile`), and `func (c *Config) CommandProfilePath() string` returning the absolute path of the command profile. `config.Load` records the directory it loaded the project config from so the path can be resolved. New sentinel `config.ErrCommandProfileMissing`.

- [ ] **Step 1: Write the failing tests**

Add to `agent-sandbox/internal/config/config_test.go`:

```go
func TestCommandProfilePathDefaultsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "command-profile.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q", got, want)
	}
}

func TestCommandProfilePathHonoursRelativeOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\ncommand_profile = \"profiles/cmd.json\"\n")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "profiles", "cmd.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q", got, want)
	}
}

func TestCommandProfilePathKeepsAbsoluteOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	abs := filepath.Join(t.TempDir(), "elsewhere.json")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\ncommand_profile = "+strconv.Quote(abs)+"\n")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CommandProfilePath(); got != abs {
		t.Errorf("CommandProfilePath() = %q, want %q", got, abs)
	}
}
```

If `writeFile` does not already exist in the test file, add it:

```go
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent-sandbox/internal/config/ -run CommandProfile -v`
Expected: FAIL — `cfg.CommandProfilePath undefined`.

- [ ] **Step 3: Add the field and the resolver**

In `agent-sandbox/internal/config/config.go`, add to `Config`:

```go
type Config struct {
	ToolMode string        `toml:"tool_mode"`
	// CommandProfile names the nono profile the command broker runs under. It
	// is written by the operator in nono's own schema, not generated: every
	// decision about commands — which may run, what each may touch, which
	// invocations are refused — lives there. An empty value means the default
	// name beside this config file.
	CommandProfile string    `toml:"command_profile"`
	MCP            MCPConfig `toml:"mcp"`
	Sandbox        SandboxConfig `toml:"sandbox"`

	// dir is the directory the project config was loaded from. A relative
	// command_profile resolves against it rather than the process working
	// directory, so the same config behaves identically however it is invoked.
	dir string
}

// defaultCommandProfileName is the file the broker's profile is read from when
// command_profile is not set.
const defaultCommandProfileName = "command-profile.json"

// CommandProfilePath is the absolute path of the nono profile the command
// broker runs under.
func (c *Config) CommandProfilePath() string {
	name := strings.TrimSpace(c.CommandProfile)
	if name == "" {
		name = defaultCommandProfileName
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(c.dir, name)
}
```

In `Load`, immediately after the project decode succeeds (step 2 of the existing
numbered comments, before the list union), record the directory:

```go
	cfg.dir = filepath.Dir(absPath(path))
```

and add the helper next to `CommandProfilePath`:

```go
// absPath makes p absolute, falling back to p when the working directory
// cannot be read — a path that stays relative is still better than none.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/internal/config/ -run CommandProfile -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Write the failing test for the missing-file error**

```go
func TestValidateRejectsMissingCommandProfile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrCommandProfileMissing) {
		t.Fatalf("Load error = %v, want ErrCommandProfileMissing", err)
	}
}
```

The two override tests must now create the file they point at, so they keep
exercising resolution rather than tripping the new error. In
`TestCommandProfilePathHonoursRelativeOverride`, after writing the config:

```go
	writeFile(t, filepath.Join(dir, "profiles", "cmd.json"), "{}")
```

In `TestCommandProfilePathKeepsAbsoluteOverride`, after writing the config:

```go
	writeFile(t, abs, "{}")
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./agent-sandbox/internal/config/ -run CommandProfile -v`
Expected: FAIL — `config.ErrCommandProfileMissing undefined`.

- [ ] **Step 7: Add the sentinel and the check**

In `agent-sandbox/internal/config/errors.go`:

```go
// ErrCommandProfileMissing fires when the nono profile the broker runs under is
// not on disk. There is deliberately no built-in fallback: a static default
// cannot absorb the host differences the capability catalog handles for the
// agent profile (/nix/store versus /usr/bin), and a profile that looks present
// but refuses every command is the worst failure mode available.
var ErrCommandProfileMissing = errors.New("command profile not found; write it, or point command_profile at it")
```

In `validate`, after the `tool_mode` switch:

```go
	if _, err := os.Stat(cfg.CommandProfilePath()); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCommandProfileMissing, cfg.CommandProfilePath())
	}
```

Add `"os"` to the imports if it is not already there.

- [ ] **Step 8: Run the whole config suite**

Run: `go test ./agent-sandbox/internal/config/ -v`
Expected: PASS. Existing tests that call `config.Load` on a fixture without a
command profile will now fail; give each of them a `command-profile.json`
beside its config file, or point `command_profile` at a file the test writes.
Fix every one before moving on.

- [ ] **Step 9: Run the full suite**

Run: `go test ./...`
Expected: PASS. Other packages' fixtures may need the same treatment.

- [ ] **Step 10: Commit**

```bash
git add agent-sandbox/internal/config
git commit -m "feat(config): resolve the operator-written command profile

What: Adds command_profile, defaulting to command-profile.json beside the
config file, resolved as an absolute path and required to exist.

Why: Everything about commands moves into a nono profile the operator
writes. A missing profile must stop launch rather than yield a session
that silently refuses every command."
```

---

### Task 2: The shell-interpreting executor

> **As built, this task's code differs from the listing below.** Three of its
> instructions were wrong and were corrected during review: the wait func must
> NOT close the interpreter's writer (the interpreter owns it and closes it when
> the stage ends); aliased stdout/stderr need a single shared pipe rather than
> two racing goroutines; and lookup must use `interp.LookPathDir(hc.Dir, hc.Env,
> args[0])` rather than `exec.LookPath`. The command sequence is
> `Start` → drain → `Wait`, and `execWaitDelay` does not exist. Read
> `agent-sandbox/internal/broker/shellexec.go` for the real shape.

**Files:**
- Create: `agent-sandbox/internal/broker/shellexec.go`
- Create: `agent-sandbox/internal/broker/shellexec_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `broker.NewShellExecutor() *broker.ShellExecutor` and its method
  `Run(ctx context.Context, command, cwd string, stdin io.Reader, stdout, stderr io.Writer) (int, error)`.
  Task 3 adapts this to the `broker.Executor` interface.

- [ ] **Step 1: Add the dependency**

```bash
go get mvdan.cc/sh/v3@v3.14.0
```

- [ ] **Step 2: Write the failing tests**

Create `agent-sandbox/internal/broker/shellexec_test.go`:

```go
package broker_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

// runShell executes command in dir and returns exit code, stdout and stderr.
// Every case is bounded: the failure this executor exists to prevent is a
// pipeline that produces its output and then never returns, and a test that
// hangs forever reports that as a timeout of the whole package rather than of
// the case that caused it.
func runShell(t *testing.T, dir, command string, stdin string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var out, errb bytes.Buffer
	// in must stay a nil *interface*, not a typed nil pointer: a nil
	// *strings.Reader assigned to an io.Reader makes a non-nil interface, and
	// the executor would take the stdin path for every case that wants none.
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	e := broker.NewShellExecutor()
	code, err := e.Run(ctx, command, dir, in, &out, &errb)
	if err != nil {
		t.Fatalf("Run(%q): %v", command, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Run(%q) did not finish within the timeout", command)
	}
	return code, out.String(), errb.String()
}

func TestShellExecutorRunsASimpleCommand(t *testing.T) {
	code, out, _ := runShell(t, t.TempDir(), "echo hello", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("stdout = %q, want %q", out, "hello")
	}
}

func TestShellExecutorReportsExitStatus(t *testing.T) {
	code, _, _ := runShell(t, t.TempDir(), "exit 3", "")
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
}

func TestShellExecutorRunsAPipeline(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "printf 'a\\nb\\nc\\n' | grep b", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "b" {
		t.Errorf("stdout = %q, want %q", out, "b")
	}
}

func TestShellExecutorPipelineWithAnEarlyExitingReaderFinishes(t *testing.T) {
	dir := t.TempDir()
	// The reader stops after one line while the writer still has output to
	// produce. Without closing the os/exec read end when the copy fails, the
	// writer blocks forever on a pipe nobody drains.
	code, out, _ := runShell(t, dir, "seq 1 200000 | head -n 1", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "1" {
		t.Errorf("stdout = %q, want %q", out, "1")
	}
}

func TestShellExecutorRedirectsStderrIntoAPipe(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "sh -c 'echo oops >&2' 2>&1 | grep oops", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("stdout = %q, want it to contain %q", out, "oops")
	}
}

func TestShellExecutorHonoursSequencingOperators(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runShell(t, dir, "false && echo yes || echo no", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "no" {
		t.Errorf("stdout = %q, want %q", out, "no")
	}
}

func TestShellExecutorWritesRedirectsRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	code, _, _ := runShell(t, dir, "echo written > out.txt", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	body, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("read out.txt: %v", err)
	}
	if strings.TrimSpace(string(body)) != "written" {
		t.Errorf("out.txt = %q, want %q", body, "written")
	}
}

func TestShellExecutorExpandsGlobsItself(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	code, out, _ := runShell(t, dir, "echo *.go", "")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "a.go b.go" {
		t.Errorf("stdout = %q, want %q", out, "a.go b.go")
	}
}

func TestShellExecutorFeedsStdinToTheFirstCommand(t *testing.T) {
	code, out, _ := runShell(t, t.TempDir(), "cat", "piped-in")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "piped-in" {
		t.Errorf("stdout = %q, want %q", out, "piped-in")
	}
}

func TestShellExecutorReportsAParseError(t *testing.T) {
	e := broker.NewShellExecutor()
	var out, errb bytes.Buffer
	code, err := e.Run(context.Background(), "echo 'unterminated", t.TempDir(), nil, &out, &errb)
	if err != nil {
		t.Fatalf("Run returned an infrastructure error for a syntax error: %v", err)
	}
	if code == 0 {
		t.Errorf("exit = 0, want non-zero for a syntax error")
	}
	if errb.Len() == 0 {
		t.Errorf("stderr is empty; a syntax error must say what is wrong")
	}
}

func TestShellExecutorReportsAMissingCommand(t *testing.T) {
	code, _, errb := runShell(t, t.TempDir(), "definitely-not-a-real-command-xyz", "")
	if code != 127 {
		t.Errorf("exit = %d, want 127", code)
	}
	if errb == "" {
		t.Errorf("stderr is empty; a missing command must say so")
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./agent-sandbox/internal/broker/ -run ShellExecutor -v`
Expected: FAIL — `undefined: broker.NewShellExecutor`.

- [ ] **Step 4: Write the executor**

Create `agent-sandbox/internal/broker/shellexec.go`:

```go
package broker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// execWaitDelay bounds how long Wait keeps draining a finished command's output
// pipes. It is a backstop for output inherited by a grandchild that outlives the
// command: without it, such a process turns a finished command into an
// unbounded hang.
const execWaitDelay = 5 * time.Second

// ShellExecutor runs one command line. It parses and evaluates the shell
// language in this process with mvdan.cc/sh and executes every simple command
// itself, which is what keeps each execution mediated: the broker runs inside a
// nono session whose command policies decide what may be executed at all, and
// handing the line to a real shell would hand that decision to the shell.
//
// Everything the shell language does with the filesystem — globbing, redirects,
// command substitution — is performed here and is therefore bounded by the
// broker's own sandbox, not by the agent's.
type ShellExecutor struct{}

// NewShellExecutor returns a ShellExecutor. It holds no state; one value serves
// every request.
func NewShellExecutor() *ShellExecutor { return &ShellExecutor{} }

// Run evaluates command with cwd as the working directory, streaming output to
// stdout and stderr, and returns the exit status of the last command.
//
// The error is non-nil only for a failure of Run itself. A syntax error, a
// command that does not exist, and a command that fails are all reported
// through the exit status with a message on stderr, because they are outcomes
// of the agent's line rather than faults of the broker.
func (e *ShellExecutor) Run(ctx context.Context, command, cwd string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "command")
	if err != nil {
		fmt.Fprintf(stderr, "agent-sandbox: %v\n", err)
		return 2, nil
	}

	runner, err := interp.New(
		interp.Dir(cwd),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandler(execHandler),
	)
	if err != nil {
		return 0, fmt.Errorf("broker: build interpreter: %w", err)
	}

	if err := runner.Run(ctx, file); err != nil {
		if status, ok := interp.IsExitStatus(err); ok {
			return int(status), nil
		}
		fmt.Fprintf(stderr, "agent-sandbox: %v\n", err)
		return 1, nil
	}
	return 0, nil
}

// execHandler is the only place this process performs an execve. Everything the
// interpreter treats as a simple command — and nothing else — arrives here.
func execHandler(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: command not found\n", args[0])
		return interp.NewExitStatus(127)
	}

	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnv(hc)
	cmd.WaitDelay = execWaitDelay

	// Never hand a child the interpreter's own pipe ends. A policy-controlled
	// command is reached through nono's shim, and the shim duplicates every fd
	// it is given and keeps the copy: pass the interpreter's pipe writer
	// straight through and it stays open after the command exits, so the next
	// stage of the pipeline never sees EOF and the pipeline hangs after
	// producing its complete output. Interposing an os/exec pipe means the shim
	// only ever duplicates that pipe, leaving the interpreter's end held by this
	// process alone — so closing it here is what ends the stage.
	waits, err := interposeOutputs(cmd, hc)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
		return interp.NewExitStatus(126)
	}
	defer func() {
		for _, wait := range waits {
			wait()
		}
	}()

	// stdin is pumped through StdinPipe rather than assigned to cmd.Stdin: with
	// cmd.Stdin set, Wait blocks until os/exec's own copier finishes, and that
	// copier sits in Read(), which nothing interrupts while the upstream end of
	// the pipeline is still open.
	if hc.Stdin != nil {
		w, perr := cmd.StdinPipe()
		if perr != nil {
			fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], perr)
			return interp.NewExitStatus(126)
		}
		go func() {
			io.Copy(w, hc.Stdin)
			w.Close()
		}()
	}

	err = cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return interp.NewExitStatus(uint8(exitStatusOf(exitErr.ProcessState)))
	}
	fmt.Fprintf(hc.Stderr, "agent-sandbox: %s: %v\n", args[0], err)
	return interp.NewExitStatus(126)
}

// interposeOutputs wires stdout and stderr through os/exec pipes, returning the
// functions that must run after the command finishes: each waits for its copy
// to drain and then closes the interpreter's end, which is what signals EOF to
// the next stage.
func interposeOutputs(cmd *exec.Cmd, hc interp.HandlerContext) ([]func(), error) {
	var waits []func()
	attach := func(w io.Writer, set func(io.Writer), pipe func() (io.ReadCloser, error)) error {
		// A real file needs no interposition: closing it is not ours to do, and
		// the shim holding a duplicate of it is harmless.
		if w == nil || w == os.Stdout || w == os.Stderr {
			set(w)
			return nil
		}
		rc, err := pipe()
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			// Closing the read end when the copy fails is what delivers EPIPE to
			// a writer whose reader has already exited (`seq … | head -n 1`).
			if _, err := io.Copy(w, rc); err != nil {
				rc.Close()
			}
		}()
		waits = append(waits, func() {
			<-done
			if c, ok := w.(io.Closer); ok {
				c.Close()
			}
		})
		return nil
	}
	if err := attach(hc.Stdout, func(w io.Writer) { cmd.Stdout = w }, cmd.StdoutPipe); err != nil {
		return nil, err
	}
	if err := attach(hc.Stderr, func(w io.Writer) { cmd.Stderr = w }, cmd.StderrPipe); err != nil {
		return nil, err
	}
	return waits, nil
}

// execEnv renders the interpreter's environment for the child. The broker's own
// environment is already filtered by nono before it starts, and each command's
// is decided by its entry in the command profile, so nothing is filtered here.
func execEnv(hc interp.HandlerContext) []string {
	var env []string
	hc.Env.Each(func(name string, v expand.Variable) bool {
		if v.IsSet() {
			env = append(env, name+"="+v.String())
		}
		return true
	})
	return env
}

// exitStatusOf maps a finished process to the status a shell user expects.
// ExitCode() is -1 for a signal death, which would surface as 255; report the
// conventional 128+signum instead.
func exitStatusOf(ps *os.ProcessState) int {
	if ps == nil {
		return 0
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
```

The import block is:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./agent-sandbox/internal/broker/ -run ShellExecutor -v`
Expected: PASS (11 tests). If `TestShellExecutorPipelineWithAnEarlyExitingReaderFinishes`
or `TestShellExecutorRedirectsStderrIntoAPipe` hangs to the timeout, the
interposition is incomplete — check that **both** stdout and stderr are wired
through `interposeOutputs` and that the read end is closed when the copy fails.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum agent-sandbox/internal/broker/shellexec.go agent-sandbox/internal/broker/shellexec_test.go
git commit -m "feat(broker): interpret the command line in process

What: Adds ShellExecutor, which parses the agent's command line with
mvdan.cc/sh and executes every simple command through one handler, wiring
stdout and stderr through os/exec pipes and pumping stdin.

Why: The broker will run inside a nono session whose command policies
decide what may be executed. Handing the line to a real shell would hand
that decision to the shell. Interposing on the streams is required
because nono's shim duplicates and retains every fd it is given, so an
interpreter pipe end passed through stays open after the writer exits."
```

---

### Task 3: Carry a command line over the wire and serve it

**Files:**
- Modify: `agent-sandbox/internal/broker/protocol.go`
- Modify: `agent-sandbox/internal/broker/client.go`
- Modify: `agent-sandbox/internal/broker/server.go`
- Delete: `agent-sandbox/internal/broker/nonoexec.go`, `agent-sandbox/internal/broker/nonoexec_test.go`, `agent-sandbox/internal/broker/commandenv_test.go`
- Create: `agent-sandbox/cmd/broker.go`
- Create: `agent-sandbox/cmd/broker_test.go`
- Modify: `agent-sandbox/internal/broker/protocol_test.go`, `agent-sandbox/internal/broker/server_test.go`, `agent-sandbox/internal/broker/network_e2e_test.go`
- Modify: `agent-sandbox/cmd/exec.go`, `agent-sandbox/internal/mcptool/handler.go`, `agent-sandbox/internal/mcptool/tool.go`, `agent-sandbox/cmd/serve.go`, `agent-sandbox/cmd/command_runner.go`, `agent-sandbox/cmd/hook.go`, `agent-sandbox/cmd/hook_test.go`, `agent-sandbox/internal/claude/settings.go`, `agent-sandbox/internal/claude/settings_test.go`
- Delete: `agent-sandbox/internal/router/` (whole directory), `agent-sandbox/cmd/allow.go`, `agent-sandbox/cmd/allow_test.go`

**Interfaces:**
- Consumes: `broker.NewShellExecutor()` and its `Run` from Task 2.
- Produces:
  - `broker.Request{Command string; Cwd string; WithStdin bool}` — `Argv []string` is gone.
  - `broker.Executor` interface becomes `Execute(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) (int, error)` with `req.Command` carrying the line (signature unchanged, meaning changed).
  - `(*broker.Client).RunCommand(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error)` — replaces `RunSandboxed`.
  - `broker.CommandRunner` interface with the same method set, moved here from `router`.
  - `broker.SandboxNotRunningHint` string constant, moved here from `router`.
  - `agent-sandbox broker --socket <path>` subcommand.
  - `cmd.runHookCore(in io.Reader, out io.Writer) error` — the `policyFile` parameter is gone, and the hook no longer emits `--policy-file`.

- [ ] **Step 1: Write the failing protocol test**

In `agent-sandbox/internal/broker/protocol_test.go`, replace the request
round-trip test with:

```go
func TestRequestRoundTripsACommandLine(t *testing.T) {
	var buf bytes.Buffer
	want := broker.Request{Command: "rg -n foo . | head -5", Cwd: "/w", WithStdin: true}
	if err := broker.WriteRequest(&buf, want); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	got, err := broker.ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
```

Use whatever the package's existing exported read/write request helpers are
named; if they are unexported, keep the test in `package broker` rather than
`package broker_test` and call them directly.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./agent-sandbox/internal/broker/ -run RoundTripsACommandLine -v`
Expected: FAIL — `unknown field Command in struct literal`.

- [ ] **Step 3: Change the wire type**

In `agent-sandbox/internal/broker/protocol.go`:

```go
// Request is the first message on a connection: what to run and where.
//
// It carries a command *line*, not an argv. The broker interprets the shell
// language itself and executes each simple command, so splitting it here would
// duplicate that work in the one place that cannot see the result.
//
// There is deliberately no environment field. The command's environment is a
// policy decision owned by the command profile: the broker's own environment is
// filtered by nono before it starts, and each command's is decided by its entry.
// A request-supplied environment could not work — the agent's nono profile
// strips those variables long before they could be reported — and must not
// work, because the request originates inside the sandbox it would configure.
type Request struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	WithStdin bool   `json:"with_stdin"`
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./agent-sandbox/internal/broker/ -run RoundTripsACommandLine -v`
Expected: PASS. The rest of the package will not compile yet; that is expected
until Step 7.

- [ ] **Step 5: Write the failing client/server test**

In `agent-sandbox/internal/broker/server_test.go`, change `echoExecutor` and the
tests that use it to speak command lines:

```go
func (e *echoExecutor) Execute(ctx context.Context, req broker.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	e.gotReq = req
	fmt.Fprintf(stdout, "ran %s in %s", req.Command, req.Cwd)
	fmt.Fprint(stderr, "warned")
	if stdin != nil {
		if b, _ := io.ReadAll(stdin); len(b) > 0 {
			fmt.Fprintf(stdout, " stdin=%s", b)
		}
	}
	return 7, nil
}

func TestClientSendsTheCommandLine(t *testing.T) {
	exec := &echoExecutor{}
	sock := startTestServer(t, exec)

	var out, errb bytes.Buffer
	code, err := broker.NewClient(sock).RunCommand(
		context.Background(), "echo hi | cat", nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if exec.gotReq.Command != "echo hi | cat" {
		t.Errorf("server saw Command = %q, want %q", exec.gotReq.Command, "echo hi | cat")
	}
	if !strings.Contains(out.String(), "echo hi | cat") {
		t.Errorf("stdout = %q, want it to carry the command", out.String())
	}
}
```

Update every other test in the file that constructs a `Request` or calls
`RunSandboxed` to use `Command` and `RunCommand`.

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./agent-sandbox/internal/broker/ -run ClientSendsTheCommandLine -v`
Expected: FAIL — `c.RunCommand undefined`.

- [ ] **Step 7: Rename the client method and delete the nono executor**

In `agent-sandbox/internal/broker/client.go`, rename `RunSandboxed` to
`RunCommand` and change its first argument:

```go
// RunCommand sends one command line to the broker and streams its output back.
func (c *Client) RunCommand(ctx context.Context, command string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
```

and inside, build the request from it:

```go
	req := Request{Command: command, Cwd: workingDir(), WithStdin: stdin != nil}
```

Move the runner interface and the hint into the broker package. Add to
`client.go`:

```go
// CommandRunner executes one command line inside the sandbox. The broker client
// is the production implementation; tests substitute their own.
type CommandRunner interface {
	RunCommand(ctx context.Context, command string, stdin io.Reader,
		stdout, stderr io.Writer) (int, error)
}

// SandboxNotRunningHint is the actionable message shown when the broker is not
// reachable. It is exported because the situation is detected before any
// command runs: `agent-sandbox exec` and the MCP server both fail to build a
// client when AGENT_SANDBOX_BROKER_SOCKET is unset, and must print this rather
// than a raw dial error.
const SandboxNotRunningHint = "command broker is not available; run Claude via `agent-sandbox claude`, which starts it automatically"
```

Delete the files the old executor lived in:

```bash
git rm agent-sandbox/internal/broker/nonoexec.go \
       agent-sandbox/internal/broker/nonoexec_test.go \
       agent-sandbox/internal/broker/commandenv_test.go
```

- [ ] **Step 8: Adapt ShellExecutor to the Executor interface**

Add to `agent-sandbox/internal/broker/shellexec.go`:

```go
// Execute satisfies Executor so the server can run a request directly. The
// server owns the transport; ShellExecutor owns the shell language.
func (e *ShellExecutor) Execute(ctx context.Context, req Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if strings.TrimSpace(req.Command) == "" {
		return 0, fmt.Errorf("broker: empty command")
	}
	return e.Run(ctx, req.Command, req.Cwd, stdin, stdout, stderr)
}
```

- [ ] **Step 9: Run the broker suite to verify it passes**

Run: `go test ./agent-sandbox/internal/broker/ -v`
Expected: PASS. `network_e2e_test.go` may reference the deleted executor; if it
does, rewrite it against `ShellExecutor` or delete it — the network behaviour it
covered is now a property of the command profile, not of Go code.

- [ ] **Step 10: Write the failing test for the broker subcommand**

Create `agent-sandbox/cmd/broker_test.go`:

```go
package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

func TestBrokerServesOnTheGivenSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")

	srv, err := startBrokerServer(sock)
	if err != nil {
		t.Fatalf("startBrokerServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	go srv.Serve()

	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var out, errb bytes.Buffer
	code, err := broker.NewClient(sock).RunCommand(
		context.Background(), "echo served", nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 (stderr=%q)", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "served" {
		t.Errorf("stdout = %q, want %q", out.String(), "served")
	}
}
```

- [ ] **Step 11: Run it to verify it fails**

Run: `go test ./agent-sandbox/cmd/ -run BrokerServes -v`
Expected: FAIL — `undefined: startBrokerServer`.

- [ ] **Step 12: Add the broker subcommand**

Create `agent-sandbox/cmd/broker.go`:

```go
package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

var brokerSocket string

// brokerCmd is the process the launcher starts inside `nono run`. It is not
// meant to be run by hand: outside that session it has no command policies
// above it, so it would execute commands with whatever the caller can reach.
var brokerCmd = &cobra.Command{
	Use:    "broker",
	Short:  "Serve commands for a sandboxed agent (started by `agent-sandbox claude`)",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE:   runBroker,
}

func init() {
	brokerCmd.Flags().StringVar(&brokerSocket, "socket", "",
		"unix socket path to serve on (required)")
	rootCmd.AddCommand(brokerCmd)
}

// startBrokerServer opens the socket and returns a server that runs each
// request through the in-process shell interpreter.
func startBrokerServer(sockPath string) (*broker.Server, error) {
	return broker.NewServer(sockPath, broker.NewShellExecutor())
}

func runBroker(cmd *cobra.Command, args []string) error {
	if brokerSocket == "" {
		return fmt.Errorf("--socket is required")
	}
	srv, err := startBrokerServer(brokerSocket)
	if err != nil {
		return err
	}
	defer srv.Close()

	// The launcher tears the session down by killing this process; handling the
	// signal is what lets the socket be removed instead of left behind.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		srv.Close()
	}()

	srv.Serve()
	return nil
}
```

- [ ] **Step 13: Run it to verify it passes**

Run: `go test ./agent-sandbox/cmd/ -run BrokerServes -v`
Expected: PASS.

- [ ] **Step 14: Point exec and the MCP tool at the broker directly**

In `agent-sandbox/cmd/exec.go`, replace `runExecCore` with:

```go
// runExecCore sends command to the broker and streams its output, returning the
// exit code. There is no routing left to do: the command profile decides what
// may run, and the broker's interpreter decides how the line is executed.
func runExecCore(ctx context.Context, command string, stdout, stderr io.Writer) int {
	client, err := broker.NewClientFromEnv()
	if err != nil {
		// The overwhelmingly common cause is running `agent-sandbox exec`
		// outside a `claude` session, so the socket variable is unset. Print the
		// actionable hint instead of the raw dial/lookup error.
		if errors.Is(err, broker.ErrBrokerUnavailable) {
			fmt.Fprintln(stderr, broker.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "command broker: %v\n", err)
		}
		return 1
	}
	code, runErr := client.RunCommand(ctx, command, nil, stdout, stderr)
	if runErr != nil {
		if errors.Is(runErr, broker.ErrBrokerUnavailable) {
			fmt.Fprintln(stderr, broker.SandboxNotRunningHint)
		} else {
			fmt.Fprintf(stderr, "%v\n", runErr)
		}
		return 1
	}
	return code
}
```

and simplify `runExec` to drop the config load and the policy-file flag:

```go
func runExec(cmd *cobra.Command, args []string) error {
	command := commandFromArgs(cmd, args)
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no command given after --")
	}
	os.Exit(runExecCore(context.Background(), command, os.Stdout, os.Stderr))
	return nil
}
```

Delete `execPolicyFile`, its flag registration, and `resolveExecConfig`.

The hook that *emits* that flag has to go in the same task, or the hook would
keep rewriting commands into `agent-sandbox exec --policy-file … -- …` against
an `exec` that no longer accepts it, breaking every Bash tool call until Task 6.
In `agent-sandbox/cmd/hook.go`, drop `hookPolicyFile`, its flag registration,
and the `policyFile` parameter of `runHookCore`, leaving:

```go
	wrapped := "agent-sandbox exec -- " + shellquote.Quote(input.ToolInput.Command)
```

In `agent-sandbox/internal/claude/settings.go`, drop the `policyFile` argument
and the `--policy-file` suffix it appends to the injected hook command, and
update `settingsJSON`'s signature and its callers. Update
`agent-sandbox/cmd/hook_test.go` and
`agent-sandbox/internal/claude/settings_test.go` to match. The snapshot itself
is still written at this point; Task 6 removes it.

In `agent-sandbox/internal/mcptool/handler.go`:

```go
// CommandRunner is the broker's execution interface, re-exported so existing
// callers keep their import.
type CommandRunner = broker.CommandRunner

type HandlerConfig struct {
	OutputDir     string
	CommandRunner CommandRunner
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}

	exitCode, runErr := cfg.CommandRunner.RunCommand(ctx, cmd, nil, files.Stdout, files.Stderr)

	closeErr := files.Close()
	if runErr != nil {
		return errorResult(runErr.Error()), nil, nil
	}
	if closeErr != nil {
		return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
	}
	return BuildResponse(exitCode, files), nil, nil
}
```

Delete the `DropRule` re-export. In `agent-sandbox/cmd/serve.go`, drop
`AllowPatterns` and `DropRules` from the `mcptool.HandlerConfig` literal and
change `router.SandboxNotRunningHint` to `broker.SandboxNotRunningHint`. In
`agent-sandbox/cmd/command_runner.go`, change `unavailableRunner`'s method to
`RunCommand(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error)`
and change `newBrokerCommandRunner` to return `broker.CommandRunner`.

- [ ] **Step 15: Delete the router and the allow list**

```bash
git rm -r agent-sandbox/internal/router
git rm agent-sandbox/cmd/allow.go agent-sandbox/cmd/allow_test.go
```

Fix the remaining compile errors in `agent-sandbox/cmd/` — `exec_test.go` and
`hook_test.go` will reference removed helpers. Rewrite `exec_test.go` around
`runExecCore` against a fake broker socket, following the pattern in
`agent-sandbox/cmd/broker_test.go`.

- [ ] **Step 16: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 17: Commit**

```bash
git add -A agent-sandbox
git commit -m "refactor(broker): send a command line and interpret it

What: The wire request carries a command line instead of an argv, the
client exposes RunCommand, the server runs it through ShellExecutor, and
a hidden `agent-sandbox broker` subcommand serves it. Deletes
internal/router and the nono-spawning executor.

Why: The broker now interprets the shell language itself, so splitting
the line before it reaches the broker duplicates that work in the one
place that cannot see the result. Routing is gone entirely: the command
profile decides what may run."
```

---

### Task 4: Launch the broker inside its own nono session

**Files:**
- Modify: `agent-sandbox/internal/claude/launch.go`
- Modify: `agent-sandbox/internal/claude/launch_test.go`
- Modify: `agent-sandbox/cmd/debug.go`

**Interfaces:**
- Consumes: `config.Config.CommandProfilePath()` (Task 1), `agent-sandbox broker --socket` (Task 3).
- Produces: `claude.BrokerArgs(cfg *config.Config, nonoPath, selfPath, sockPath, workdir string) []string` returning the full `nono run …` argv for the broker session.

- [ ] **Step 1: Write the failing test**

In `agent-sandbox/internal/claude/launch_test.go`:

```go
func TestBrokerArgsRunsTheBrokerUnderTheCommandProfile(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	cfg := loadConfigWithCommandProfile(t, dir, profile)

	args := claude.BrokerArgs(cfg, "/usr/bin/nono", "/opt/agent-sandbox/bin/agent-sandbox",
		"/run/b.sock", "/work/project")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run", "--silent",
		"--profile " + profile,
		"--workdir /work/project",
		"--allow-unix-socket-bind /run/b.sock",
		"-- /opt/agent-sandbox/bin/agent-sandbox broker --socket /run/b.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("BrokerArgs() = %q\nmissing %q", joined, want)
		}
	}
	if strings.Contains(joined, "--allow-cwd") {
		t.Errorf("BrokerArgs() grants --allow-cwd; the working directory comes from the profile's $WORKDIR")
	}
}
```

Add the fixture helper:

```go
// loadConfigWithCommandProfile writes a minimal project config in dir pointing
// at profile and loads it, so the test exercises the same resolution the
// launcher uses rather than a hand-built Config.
func loadConfigWithCommandProfile(t *testing.T, dir, profile string) *config.Config {
	t.Helper()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode = \"hook\"\ncommand_profile = " + strconv.Quote(profile) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./agent-sandbox/internal/claude/ -run BrokerArgs -v`
Expected: FAIL — `undefined: claude.BrokerArgs`.

- [ ] **Step 3: Build the broker argv**

In `agent-sandbox/internal/claude/launch.go`:

```go
// BrokerArgs builds the `nono run` argv for the command broker's session.
//
// The broker is a sibling of the agent's sandbox, not a child of it: nono
// refuses to nest, and the broker must be the session entrypoint so its command
// policies apply to everything it executes. It is deliberately not given
// --allow-cwd; the working directory reaches the profile through --workdir,
// which is what $WORKDIR expands to inside it.
func BrokerArgs(cfg *config.Config, nonoPath, selfPath, sockPath, workdir string) []string {
	return []string{
		"nono", "run", "--silent",
		"--profile", cfg.CommandProfilePath(),
		"--workdir", workdir,
		"--allow-unix-socket-bind", sockPath,
		"--",
		selfPath, "broker", "--socket", sockPath,
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./agent-sandbox/internal/claude/ -run BrokerArgs -v`
Expected: PASS.

- [ ] **Step 5: Start the broker as a child process**

Replace `startCommandBroker` in `agent-sandbox/internal/claude/launch.go`:

```go
// startCommandBroker launches the broker in its own nono session and returns
// the socket path plus a cleanup that stops it.
//
// The broker no longer runs in this process. It runs inside a sandbox whose
// command policies govern everything it executes, which is the whole point: a
// broker outside the sandbox would execute commands with the launcher's own
// reach.
func startCommandBroker(cfg *config.Config) (string, func(), error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	selfPath, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate agent-sandbox: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("getwd: %w", err)
	}
	sockPath, err := BrokerSocketPath()
	if err != nil {
		return "", nil, err
	}
	// A socket left by a killed run would make the broker's bind fail forever.
	if rmErr := os.Remove(sockPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return "", nil, fmt.Errorf("remove stale socket: %w", rmErr)
	}

	args := BrokerArgs(cfg, nonoPath, selfPath, sockPath, cwd)
	cmd := exec.Command(nonoPath, args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start command broker: %w", err)
	}

	if err := waitForSocket(sockPath, brokerStartTimeout); err != nil {
		cmd.Process.Kill()
		return "", nil, err
	}

	cleanup := func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			cmd.Wait()
		}
		os.Remove(sockPath)
	}
	return sockPath, cleanup, nil
}

// brokerStartTimeout bounds how long the launcher waits for the broker's socket
// to appear. A sandbox that cannot start fails here rather than leaving Claude
// running against a broker that will never answer.
const brokerStartTimeout = 15 * time.Second

// waitForSocket polls until path exists or the deadline passes. Polling is used
// rather than a readiness handshake because the broker is behind a sandbox
// boundary: there is no shared channel to signal on until the socket itself.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("command broker did not start within %s; run `agent-sandbox doctor`", timeout)
}
```

Add `"syscall"` and `"time"` to the imports; remove the now-unused
`sandboxhost.ResolveShell` and `broker.NewNonoExecutor` references.

- [ ] **Step 6: Update debug to print the broker invocation**

In `agent-sandbox/cmd/debug.go`, after the existing agent invocation is printed,
also print the broker one, so `agent-sandbox debug` still shows every process
the launcher starts:

```go
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent-sandbox: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "command broker:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join(
		claude.BrokerArgs(cfg, "nono", selfPath, brokerSocket, cwd), " "))
```

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS. `launch_test.go`'s `runDeps` fake for `startBroker` still
applies; the real function is only exercised by the new `BrokerArgs` test.

- [ ] **Step 8: Commit**

```bash
git add agent-sandbox
git commit -m "feat(claude): start the broker in its own nono session

What: The launcher spawns `nono run --profile <command profile> --
agent-sandbox broker` as a sibling of the agent's sandbox, waits for its
socket, and stops it on teardown. `debug` prints that invocation too.

Why: A broker in the launcher process executes commands with the
launcher's own reach. Inside its own session it is a policy-controlled
command, so the command profile governs everything it runs."
```

---

### Task 5: Retire the routing config and the shell profile

**Files:**
- Modify: `agent-sandbox/internal/config/config.go`, `agent-sandbox/internal/config/errors.go`, `agent-sandbox/internal/config/config_test.go`
- Modify: `agent-sandbox/internal/sandboxhost/sandboxhost.go`, `agent-sandbox/internal/sandboxhost/catalog.go`, `agent-sandbox/internal/sandboxhost/sandboxhost_test.go`
- Modify: `agent-sandbox.toml`

**Interfaces:**
- Consumes: `config.ErrCommandProfileMissing` (Task 1).
- Produces: `config.SandboxConfig{Agent AgentConfig}` — `Shared` and `Shell` are gone, and `AgentConfig` is `HostConfig` with no extra fields. New sentinels `config.ErrMovedCommandTiers`, `config.ErrMovedSharedToAgent`, `config.ErrMovedShellToProfile`.

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadRejectsAllowCommands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n[sandbox.agent]\nallow_commands = [\"go *\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedCommandTiers) {
		t.Fatalf("Load error = %v, want ErrMovedCommandTiers", err)
	}
}

func TestLoadRejectsDropCommands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n[sandbox.agent]\ndrop_commands = [{ pattern = \"git *\" }]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedCommandTiers) {
		t.Fatalf("Load error = %v, want ErrMovedCommandTiers", err)
	}
}

func TestLoadRejectsSharedSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n[sandbox.shared]\ncapabilities = [\"go\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedSharedToAgent) {
		t.Fatalf("Load error = %v, want ErrMovedSharedToAgent", err)
	}
}

func TestLoadRejectsShellSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "tool_mode = \"hook\"\n[sandbox.shell]\nallow_domains = [\"example.com\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedShellToProfile) {
		t.Fatalf("Load error = %v, want ErrMovedShellToProfile", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent-sandbox/internal/config/ -run "LoadRejects" -v`
Expected: FAIL — the sentinels are undefined and the keys still decode.

- [ ] **Step 3: Remove the fields and add the sentinels**

In `agent-sandbox/internal/config/config.go`:

```go
// SandboxConfig is the host access agent-sandbox generates a profile for: the
// launched agent, and nothing else. Commands are governed by the operator's
// command profile, which agent-sandbox does not generate and does not read.
type SandboxConfig struct {
	Agent HostConfig `toml:"agent"`
}
```

Delete `AgentConfig`, `ShellConfig`, and `DropRule`. In `Load`, delete the
`userAllowCommands` / `userDropCommands` / `userAllowDomains` snapshots and
their unions, and reduce the host-section union to `cfg.Sandbox.Agent`. In
`validate`, delete the `DropCommands` loop and reduce the `allow_env` loop to
the single agent section.

In `agent-sandbox/internal/config/errors.go`:

```go
var ErrMovedCommandTiers = errors.New("sandbox.agent.allow_commands / drop_commands are no longer supported: which commands may run, and which invocations are refused, are now command_policies entries in the command profile")
var ErrMovedSharedToAgent = errors.New("sandbox.shared is no longer supported: it existed to feed both profiles, and only the agent profile is generated now; write its keys under [sandbox.agent]")
var ErrMovedShellToProfile = errors.New("sandbox.shell is no longer supported: the sandbox brokered commands run in is the command profile, which you write")
```

Delete `ErrDropRuleMissingPattern`, `ErrMovedCommandRouting`, and
`ErrMovedSharedSection`. Add to `checkDeprecated`, before the existing
`sandbox.command` checks:

```go
	if md.IsDefined("sandbox", "agent", "allow_commands") || md.IsDefined("sandbox", "agent", "drop_commands") {
		return ErrMovedCommandTiers
	}
	if md.IsDefined("sandbox", "shared") {
		return ErrMovedSharedToAgent
	}
	if md.IsDefined("sandbox", "shell") {
		return ErrMovedShellToProfile
	}
```

- [ ] **Step 4: Run the config suite**

Run: `go test ./agent-sandbox/internal/config/ -v`
Expected: PASS once fixtures using the removed sections are updated.

- [ ] **Step 5: Remove the shell profile from sandboxhost**

In `agent-sandbox/internal/sandboxhost/sandboxhost.go`, delete `ResolveShell`,
`shellNetwork`, `ShellGrants`, `ShellFilesystemGrants`, `ShellAllowDomains`,
`withDomains`, and the `workdir` and `network` fields of `sideOptions`. Change
`Resolve` to expand a single section:

```go
// Resolve builds the profile for the launched agent from [sandbox.agent], on
// the given agent's nono base profile. It is the only profile agent-sandbox
// generates: the sandbox commands run in is the operator's command profile.
func Resolve(cfg *config.Config, agent string) (*Resolved, error) {
	base, ok := agentBases[agent]
	if !ok {
		return nil, fmt.Errorf("sandboxhost: unknown agent %q", agent)
	}
	return expand(
		[]config.HostConfig{cfg.Sandbox.Agent},
		sideOptions{
			extends:  base.extends,
			metaName: base.metaName,
			extraEnv: agentOnlyEnv,
			emitDeny: true,
		},
	)
}
```

In `agent-sandbox/internal/sandboxhost/catalog.go`, delete the `domains` field
from `capability`, every `domains:` line in the catalog, and
`shellNetworkProfile`. Delete the `domains` accumulation in `expand`, the `Network` field of
`nonoProfile`, and the `profileNetwork` type: only the shell profile ever set
them, and the shell profile is going away.

- [ ] **Step 6: Run the sandboxhost suite**

Run: `go test ./agent-sandbox/internal/sandboxhost/ -v`
Expected: PASS once tests for the deleted functions are removed and
capability-network assertions are dropped.

- [ ] **Step 7: Rewrite this repository's own config**

Rewrite `agent-sandbox.toml` so it carries only what remains: `tool_mode`,
`command_profile` if the default name is not used, and `[sandbox.agent]`. Fold
what `[sandbox.shared]` granted the agent into `[sandbox.agent]`; everything it
granted commands is Task 8's command profile.

- [ ] **Step 8: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add -A agent-sandbox agent-sandbox.toml
git commit -m "refactor(config): drop routing and the generated shell profile

What: Removes allow_commands, drop_commands, [sandbox.shared] and
[sandbox.shell], leaving [sandbox.agent] as the only generated profile's
source. Each removed key fails loudly, naming where it moved. Deletes
ResolveShell and the capability network domains that only fed it.

Why: Everything about commands now lives in the operator's command
profile. A key that quietly stopped having an effect would be worse than
one that stops the launch."
```

---

### Task 6: Delete the safe wrappers and the policy snapshot

**Files:**
- Delete: `agent-sandbox/internal/safe/` (whole directory), `agent-sandbox/cmd/safe.go`, `agent-sandbox/cmd/safe_git.go`, `agent-sandbox/cmd/safe_git_test.go`, `agent-sandbox/cmd/safe_docker_compose.go`, `agent-sandbox/cmd/safe_docker_compose_test.go`
- Modify: `agent-sandbox/internal/policysnapshot/policysnapshot.go`, `agent-sandbox/internal/policysnapshot/policysnapshot_test.go`
- Modify: `agent-sandbox/internal/claude/launch.go`, `agent-sandbox/cmd/debug.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `policysnapshot.StateDir()` only — `Write` and `Load` are gone. `claude.BuildArgs` loses its `snapshotPath` parameter.

- [ ] **Step 1: Delete the tests for the functions that are going**

In `agent-sandbox/internal/policysnapshot/policysnapshot_test.go`, delete the
tests covering `Write` and `Load`. Nothing replaces them: the snapshot they
tested no longer has anything to freeze.

- [ ] **Step 2: Run the suite to see what still depends on them**

Run: `go test ./...`
Expected: PASS — the tests are gone but the functions remain, so nothing breaks
yet. This step exists to record the starting point.

- [ ] **Step 3: Remove the snapshot plumbing**

In `agent-sandbox/internal/policysnapshot/policysnapshot.go`, delete `Write` and
`Load`, keeping `StateDir` and its doc comment — `cmd/doctor.go` and the broker
socket path both use it.

In `agent-sandbox/internal/claude/launch.go`, delete `writeSnapshot` from
`runDeps`, the snapshot block in `run`, the `--read-file snapshotPath` grant in
`BuildArgs`, and the `snapshotPath` parameter; update `launch_test.go`'s
`runDeps` fakes and `BuildArgs` callers to match. In `agent-sandbox/cmd/debug.go`,
delete the snapshot block.

The hook and the injected settings already stopped emitting `--policy-file` in
Task 3, so nothing here changes what the agent's commands are rewritten into.

- [ ] **Step 4: Run the suite to verify it still passes**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Delete the safe wrappers**

```bash
git rm -r agent-sandbox/internal/safe
git rm agent-sandbox/cmd/safe.go \
       agent-sandbox/cmd/safe_git.go agent-sandbox/cmd/safe_git_test.go \
       agent-sandbox/cmd/safe_docker_compose.go agent-sandbox/cmd/safe_docker_compose_test.go
```

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS. `agentconfig` will not compile — it lists the `safe`
subcommands; remove that from its view struct and template now, and finish the
template in Task 8.

- [ ] **Step 7: Commit**

```bash
git add -A agent-sandbox
git commit -m "refactor: remove the safe wrappers and the policy snapshot

What: Deletes internal/safe with both wrappers, their subcommands, and
policysnapshot.Write/Load with the --policy-file flag that carried them.
StateDir stays; doctor and the broker socket path use it.

Why: git's dangerous invocations are invocation_policy rules in the
command profile, where they cannot be evaded by wrapping. The compose
wrapper's other rules read the resolved YAML, which no argv matcher can
see, so command control narrows to what nono expresses. The snapshot
froze allow_commands and drop_commands, which no longer exist."
```

---

### Task 7: Check the command profile in doctor

**Files:**
- Modify: `agent-sandbox/cmd/doctor.go`
- Modify: `agent-sandbox/cmd/doctor_test.go`

**Interfaces:**
- Consumes: `config.Config.CommandProfilePath()` (Task 1).
- Produces: `checkCommandProfile(cfg *config.Config) checkResult` and `checkToolSandbox(ctx context.Context) checkResult`, both appended to `runDoctor`'s results.

- [ ] **Step 1: Write the failing tests**

```go
func TestCheckCommandProfileFailsWhenValidateRejects(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	if err := os.WriteFile(profile, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubRunCommand(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("JSON syntax invalid"), fmt.Errorf("exit status 1")
	})
	defer restore()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false for a profile nono rejects")
	}
	if got.hint == "" {
		t.Errorf("a failing check must carry a hint")
	}
}

func TestCheckCommandProfileFailsWhenTheBinaryIsWritable(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	// The broker binary sitting inside the directory the profile grants
	// read+write is exactly what nono refuses at startup, with a message the
	// operator will not see until every command has already failed.
	body := `{"filesystem":{"allow":["` + dir + `"]}}`
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	restore := stubSelfPath(filepath.Join(dir, "agent-sandbox"))
	defer restore()

	got := checkCommandProfile(configWithProfile(t, dir, profile))
	if got.ok {
		t.Errorf("checkCommandProfile ok = true, want false when the binary is writable through the profile")
	}
}
```

Add the fixtures, following whatever stub pattern `doctor_test.go` already uses
for `lookPath` / `runCommand`:

```go
func configWithProfile(t *testing.T, dir, profile string) *config.Config {
	t.Helper()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	body := "tool_mode = \"hook\"\ncommand_profile = " + strconv.Quote(profile) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func stubSelfPath(path string) func() {
	prev := selfPath
	selfPath = func() (string, error) { return path, nil }
	return func() { selfPath = prev }
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent-sandbox/cmd/ -run CheckCommandProfile -v`
Expected: FAIL — `undefined: checkCommandProfile`.

- [ ] **Step 3: Add the checks**

In `agent-sandbox/cmd/doctor.go`:

```go
// selfPath is os.Executable, indirected so tests can place the broker binary
// anywhere without moving a real file.
var selfPath = os.Executable

// checkCommandProfile reports whether the profile the broker will run under is
// present, accepted by nono, and does not grant write access to the broker's own
// binary. Each of these otherwise produces a session that refuses every command
// with an error the agent cannot act on: nono's own failure arrives on the
// broker's stderr long after the launcher has returned.
func checkCommandProfile(cfg *config.Config) checkResult {
	r := checkResult{name: "command profile"}
	path := cfg.CommandProfilePath()
	r.details = append(r.details, "path: "+path)

	if _, err := os.Stat(path); err != nil {
		r.hint = "write the profile, or point command_profile at it"
		return r
	}
	if out, err := runCommand(context.Background(), "nono", "profile", "validate", path); err != nil {
		r.details = append(r.details, "nono profile validate: "+strings.TrimSpace(string(out)))
		r.hint = "fix the profile until `nono profile validate` passes"
		return r
	}
	self, err := selfPath()
	if err == nil {
		writable, werr := profileGrantsWrite(path, self)
		if werr == nil && writable {
			r.details = append(r.details, "broker binary: "+self)
			r.hint = "move the agent-sandbox binary outside every path the profile grants write access to; " +
				"nono refuses a policy command binary it considers replaceable"
			return r
		}
	}
	r.ok = true
	return r
}

// profileGrantsWrite reports whether binPath falls under one of the profile's
// filesystem.allow entries. It reads only that list: it is the grant that made
// nono refuse to start during the design measurements, and a fuller model of
// nono's own trust check belongs in nono, not here.
func profileGrantsWrite(profilePath, binPath string) (bool, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return false, err
	}
	var p struct {
		Filesystem struct {
			Allow []string `json:"allow"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return false, err
	}
	bin := filepath.Clean(binPath)
	for _, dir := range p.Filesystem.Allow {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || strings.Contains(dir, "$") {
			continue // $WORKDIR and friends are resolved by nono, not here
		}
		if rel, rerr := filepath.Rel(dir, bin); rerr == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}
```

Wire it into `runDoctor`, which now needs the config:

```go
	cfg, cfgErr := config.Load(configPath)
	results := []checkResult{
		checkNono(ctx),
		checkBrokerSocketDir(),
	}
	if cfgErr != nil {
		results = append(results, checkResult{
			name: "command profile",
			hint: "fix the config first: " + cfgErr.Error(),
		})
	} else {
		results = append(results, checkCommandProfile(cfg))
	}
```

Add `"encoding/json"`, `"path/filepath"` and the config import.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./agent-sandbox/cmd/ -run CheckCommandProfile -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent-sandbox/cmd
git commit -m "feat(doctor): check the command profile before a session starts

What: doctor now reports whether the command profile exists, passes
`nono profile validate`, and leaves the broker binary outside every path
it grants write access to.

Why: Each of these fails inside the broker's own sandbox, on a stderr the
launcher has already stopped watching. Without the check the session
looks healthy and refuses every command."
```

---

### Task 8: Describe the new world to the agent, and ship a worked profile

**Files:**
- Modify: `agent-sandbox/internal/agentconfig/explain.tmpl`
- Modify: `agent-sandbox/internal/agentconfig/agentconfig.go`, `agent-sandbox/internal/agentconfig/agentconfig_test.go`
- Create: `command-profile.json`
- Modify: `README.md`, `README.ja.md`

**Interfaces:**
- Consumes: `config.Config.CommandProfilePath()` (Task 1), the two-tier model (Task 3).
- Produces: `agentconfig.Explain(cfg *config.Config, configPath string) string` — the `safe ...SafeCommand` variadic is gone.

- [ ] **Step 1: Write the failing test**

```go
func TestExplainNamesTheCommandProfileAndBothTiers(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "command-profile.json")
	writeProfile(t, profile, `{
	  "command_policies": {
	    "commands": {
	      "agent-sandbox": { "can_use": ["git"],
	        "from": { "session": { "sandbox": { "exec_paths": ["/usr/bin"] } } } },
	      "git": { "from": { "agent-sandbox": { "invocation_policy": { "deny": [
	        { "argv": { "contains": ["--force"] }, "reason": "force push is disabled in this sandbox" }
	      ] } } } }
	    }
	  }
	}`)
	got := agentconfig.Explain(configWithProfile(t, dir, profile), filepath.Join(dir, "agent-sandbox.toml"))

	for _, want := range []string{
		profile,
		"git",
		"force push is disabled in this sandbox",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() is missing %q\n---\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"allow_commands", "drop_commands", "safe git", "[sandbox.shell]"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Explain() still mentions %q", unwanted)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./agent-sandbox/internal/agentconfig/ -run ExplainNames -v`
Expected: FAIL — `Explain` still takes the `safe` variadic and prints the old
sections.

- [ ] **Step 3: Rewrite the view and the template**

In `agent-sandbox/internal/agentconfig/agentconfig.go`, replace `explainView`'s
`Allow`, `Drop`, `Safe`, `AllowDomains`, `OutsideWrite`, `OutsideRead` and
`Resolved` fields with what the command profile yields:

```go
type explainView struct {
	Hook        bool
	ConfigPath  string
	ProfilePath string
	// PolicyCommands are the commands with their own child sandbox and argv
	// rules; FloorCommands run in the broker's own sandbox. An agent that knows
	// which is which can tell a refusal from a bug.
	PolicyCommands []policyCommandView
	FloorPaths     []string
}

type policyCommandView struct {
	Name    string
	Denials []string // each is "<matcher>: <reason>"
}
```

Parse the profile in `Explain`, reading only `command_policies.commands`: for
each entry, the name, and for each `from.<caller>.invocation_policy.deny` rule,
its `reason` (falling back to the rendered matcher when `reason` is absent). The
broker's own entry contributes `FloorPaths` from its `exec_paths`.

Rewrite `explain.tmpl` around: how commands run (the broker interprets the line;
there is no shell), the two tiers with their members, the denials with their
reasons, and where the profile lives. Delete the schema block describing
`allow_commands`, `drop_commands`, `[sandbox.shared]` and `[sandbox.shell]`;
replace it with a pointer to the command profile and a note that editing it
takes effect at the next `agent-sandbox claude`.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./agent-sandbox/internal/agentconfig/ -run ExplainNames -v`
Expected: PASS.

- [ ] **Step 5: Write this repository's command profile**

Create `command-profile.json` covering what the agent actually runs here: `go`,
`rg`, `git`, the coreutils this repo's workflows use, `mise`, and
`agent-sandbox` itself. Follow the spec's shape section. Rules:

- the broker entry pins `executable` to the installed `agent-sandbox`, lists
  `git` in `can_use`, and puts every floor command's directory in `exec_paths`;
- `git` is a policy command absent from `exec_paths`, with `invocation_policy`
  denials translated from the deleted wrapper: `--force`, `--hard`,
  `--no-verify`, `reflog expire`, `stash drop`, and `-c` wholesale;
- no `bash` or `sh` in either tier;
- `$WORKDIR` wherever the working directory is meant, never a literal path;
- the top-level `network` section carries `network_profile` and the domains;
  a command reaches the network only with `"network": {"allow_all": true}`.

Verify before committing:

```bash
nono profile validate command-profile.json
```

- [ ] **Step 6: Verify the profile actually runs this repository's workflow**

```bash
mise run build
agent-sandbox doctor
```

Then, in a session started by `agent-sandbox claude`, confirm by hand:
`go test ./...` runs; `rg -n Executor agent-sandbox | head -3` runs;
`git status --short` runs; `git push --force` is refused with its reason;
`bash -c 'echo x'` is refused at `execve`.

Record any command the profile had to gain in the commit body — that list is the
enumeration cost the spec asks to measure.

- [ ] **Step 7: Update the READMEs**

Replace every mention of `allow_commands`, `drop_commands`, `[sandbox.shared]`,
`[sandbox.shell]`, `safe git` and `safe docker-compose` with the command-profile
model: two tiers, where the profile lives, and that a command absent from both
tiers cannot run. `README.ja.md` gets the same content in Japanese.

- [ ] **Step 8: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "docs: describe the command profile to the agent and the operator

What: `ai explain` now names the profile, both command tiers and every
invocation_policy denial with its reason. Adds this repository's own
command profile and rewrites the README sections that described routing.

Why: An agent that cannot tell a policy refusal from a bug retries it.
The worked profile is also the artifact that shows what enumerating every
runnable command actually costs."
```

---

## Self-Review

**Spec coverage.** Every section of the spec maps to a task: the two-tier model
and the interpreter to Tasks 2–3; the command profile's location, `$WORKDIR` and
network rules to Tasks 1 and 8; the launcher's two sibling sandboxes to Task 4;
stream ownership to Task 2; every entry in *What is deleted* to Tasks 3, 5 and 6;
*Measured constraints* to Task 7's checks and Task 8's profile; *Follow-on work*
to Tasks 7 and 8. The spec's upstream reports are not code and are not planned
here.

**Naming consistency.** `RunCommand` is used in the client, the `CommandRunner`
interface, the MCP handler and the fake runner. `CommandProfilePath()` is the
single accessor, used by the launcher, doctor and `ai explain`. `ShellExecutor`
exposes `Run` for direct use and `Execute` for the `Executor` interface.

**Known ordering constraint.** Task 3 is the pivot: the wire type, the client
method and the router deletion have to land together or the tree does not
compile. Its steps are ordered so the protocol test passes before the rest of
the package is repaired.
