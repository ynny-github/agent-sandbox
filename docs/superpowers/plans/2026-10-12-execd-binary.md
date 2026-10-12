# execd As Its Own Binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship execd as a standalone `execd` binary (`serve` / `exec`), and make agent-sandbox launch and talk to it instead of carrying its own `execd` and `exec` subcommands.

**Architecture:** `internal/execd` already holds the server, protocol and client; it gains the CLI client loop (`RunCLI`) that today lives in `internal/cli/exec.go`. A new stdlib-`flag` main at `cmd/execd` exposes `serve` and `exec`. The launcher resolves `execd` on PATH once per launch (inside `startExecd`), runs `<execd> serve --socket` under the command profile, and grants the same path to the agent so the hook's `execd exec -- …` can run.

**Tech Stack:** Go 1.25, stdlib `flag`, `mvdan.cc/sh/v3` (unchanged), nono 0.78.

**Spec:** `docs/superpowers/specs/2026-10-12-execd-binary-design.md`

## Global Constraints

- Every binary lives at `cmd/<name>/`; the new one is `cmd/execd/main.go`, installed binary name `execd`.
- `cmd/execd` uses the standard library `flag` package, not cobra.
- execd reads no config file and does not import `internal/config`; flags and `AGENT_SANDBOX_EXECD_SOCKET` are its whole input.
- `AGENT_SANDBOX_EXECD_SOCKET` is not renamed.
- No protocol version handshake.
- The launcher finds execd with `exec.LookPath("execd")`, then `filepath.EvalSymlinks`. Not found → error naming `go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`.
- Historical documents under `docs/superpowers/` are not rewritten.
- Commits follow Conventional Commits (`.claude/rules/git-commit.md`); lefthook validates the title.
- Run `go test ./...` from the repository root. e2e tests need `-tags e2e` and a real nono.

## Review Focus

1. `execd` missing from PATH at launch → the launch fails with the install hint, and the shell wrapper written earlier is still cleaned up (pinned in Task 2, `TestRun_ExecdMissing_DoesNotLaunch`).
2. `execd` on PATH is a symlink (mise shims, `~/.local/bin` links) → both grants and the entrypoint name the resolved target, because Landlock checks the target (pinned in Task 2, `TestResolveExecd_ResolvesSymlinks`).
3. The command profile grants write to execd but not to agent-sandbox (or the reverse) → doctor fails either way (pinned in Task 4, two tests).
4. `execd exec` with no command after `--` → usage error, exit 2, nothing sent (pinned in Task 1, `TestExec_NoCommand`).
5. `execd serve` without `--socket` → usage error, exit 2, nothing bound (pinned in Task 1, `TestServe_RequiresSocket`).

---

### Task 1: `cmd/execd` binary and the shared client loop

**Files:**
- Create: `internal/execd/cli.go`
- Create: `internal/execd/cli_test.go` (package `execd_test`)
- Create: `internal/execd/cli_internal_test.go` (package `execd`)
- Create: `cmd/execd/main.go`
- Create: `cmd/execd/main_test.go`
- Modify: `internal/cli/exec.go` (delegate to `execd.RunCLI`, drop `runExecCore` and `twoStageSignals`)
- Modify: `internal/execd/protocol.go:330-334` (message names the right binary)
- Delete: `internal/cli/exec_test.go` (its cases move to `internal/execd/cli_test.go`)

**Interfaces:**
- Produces: `func RunCLI(ctx context.Context, command string, timeout time.Duration, stdio Stdio) int` in package `execd`.
- Produces: binary `execd` with `serve --socket <path>` and `exec [--timeout d] -- <cmd>`.

- [ ] **Step 1: Move the client-loop tests into `internal/execd`**

Create `internal/execd/cli_test.go`. It reuses `startTestServer`, `outFile` and `devNull` from `server_test.go` (same package `execd_test`).

```go
package execd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

// serveShell starts a real ShellExecutor-backed server and points
// AGENT_SANDBOX_EXECD_SOCKET at it, so RunCLI runs end to end without nono.
func serveShell(t *testing.T) {
	t.Helper()
	t.Setenv(execd.SocketEnvVar, startTestServer(t, execd.NewShellExecutor()))
}

func TestRunCLI_Success(t *testing.T) {
	serveShell(t)
	out, readOut := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), "echo hello", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr=%q)", code, readErr())
	}
	if !strings.Contains(readOut(), "hello") {
		t.Errorf("stdout = %q, want it to contain hello", readOut())
	}
}

// Running the client outside a session leaves the socket variable unset. That
// is the most likely way a user meets this failure, and it must produce the
// actionable hint rather than a raw dial/lookup error.
func TestRunCLI_NoSocket_ShowsHint(t *testing.T) {
	t.Setenv(execd.SocketEnvVar, "")
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), "true", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(readErr(), execd.SandboxNotRunningHint) {
		t.Errorf("stderr = %q, want the actionable execd hint", readErr())
	}
	if strings.Contains(readErr(), "exec daemon:") {
		t.Errorf("stderr = %q, want the hint instead of the raw setup error", readErr())
	}
}

func TestRunCLI_NonZeroExit(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, _ := outFile(t)
	code := execd.RunCLI(context.Background(), "exit 3", 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunCLI_Timeout(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	start := time.Now()
	code := execd.RunCLI(context.Background(), "sleep 30", 400*time.Millisecond,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 124 {
		t.Errorf("exit code = %d, want 124 (stderr=%q)", code, readErr())
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v; want it bounded by the timeout", d)
	}
	if !strings.Contains(readErr(), "timed out") {
		t.Errorf("stderr = %q, want it to say the command timed out", readErr())
	}
}

func TestRunCLI_ParseFailure(t *testing.T) {
	serveShell(t)
	out, _ := outFile(t)
	errFile, readErr := outFile(t)
	code := execd.RunCLI(context.Background(), `echo "hi`, 0,
		execd.Stdio{In: devNull(t), Out: out, Err: errFile})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if readErr() == "" {
		t.Error("stderr should describe the parse error")
	}
}
```

Create `internal/execd/cli_internal_test.go` with `package execd` and the body of `TestTwoStageSignals_FirstRelaysSecondEscapes` copied verbatim from `internal/cli/exec_test.go:156-209` (doc comment included; replace "runExecCore" with "RunCLI" in the comment). Imports: `bytes`, `os`, `strings`, `syscall`, `testing`, `time`.

Delete `internal/cli/exec_test.go`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/execd/ -run 'RunCLI|TwoStage'`
Expected: FAIL to compile — `undefined: execd.RunCLI`, `undefined: twoStageSignals`.

- [ ] **Step 3: Move the implementation**

Create `internal/execd/cli.go`. Its body is `runExecCore` and `twoStageSignals` moved from `internal/cli/exec.go`, **carrying every existing comment over verbatim**, with exactly these changes:

1. `runExecCore(ctx, command, stdio)` becomes exported `RunCLI(ctx context.Context, command string, timeout time.Duration, stdio Stdio) int`, and the read of the package var `execTimeout` becomes the `timeout` parameter: `TimeoutMs: int(timeout.Milliseconds())`.
2. Package-qualified names lose their `execd.` prefix (`NewClientFromEnv`, `ErrExecdUnavailable`, `SandboxNotRunningHint`, `Stdio`, `RunOptions`).
3. In comments, "`agent-sandbox exec`" becomes "`execd exec`".
4. The second-signal message prefix changes from `"agent-sandbox exec: second signal (%s): "` to `"execd exec: second signal (%s): "`.

Header:

```go
package execd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)
```

Then shrink `internal/cli/exec.go`: delete `runExecCore`, `twoStageSignals` and their comments, and change `runExec` to:

```go
func runExec(cmd *cobra.Command, args []string) error {
	command := commandFromArgs(cmd, args)
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no command given after --")
	}
	os.Exit(execd.RunCLI(context.Background(), command, execTimeout,
		execd.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
	return nil
}
```

Remove imports that become unused (`errors`, `io`, `os/signal`, `syscall`); `go build ./...` names them.

In `internal/execd/protocol.go:330-334`, change the comment's "an older agent-sandbox binary" to "an older execd client", and the message to:

```go
	noFDs := fmt.Errorf("execd: no file descriptors arrived with the request. " +
		"This usually means the execd client sending it is older than the " +
		"execd serving it — restart the session so both come from the same build")
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/execd/ ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Write the failing binary tests**

Create `cmd/execd/main_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

func TestRun_UnknownSubcommand(t *testing.T) {
	if code := run([]string{"bogus"}); code != 2 {
		t.Errorf("run(bogus) = %d, want 2", code)
	}
	if code := run(nil); code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
}

func TestServe_RequiresSocket(t *testing.T) {
	if code := run([]string{"serve"}); code != 2 {
		t.Errorf("run(serve) = %d, want 2 without --socket", code)
	}
}

func TestExec_NoCommand(t *testing.T) {
	if code := run([]string{"exec", "--"}); code != 2 {
		t.Errorf("run(exec --) = %d, want 2", code)
	}
}

// TestExec_ReturnsTheCommandsStatus drives the real flag parsing and the real
// client loop against an in-process server: the binary's whole job on this
// path is to hand everything after -- to RunCLI and exit with its status.
func TestExec_ReturnsTheCommandsStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "brk") // short: unix socket paths cap near 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")
	srv, err := execd.NewServer(sock, execd.NewShellExecutor())
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	t.Setenv(execd.SocketEnvVar, sock)

	if code := run([]string{"exec", "--timeout", "5s", "--", "exit", "3"}); code != 3 {
		t.Errorf("run(exec -- exit 3) = %d, want 3", code)
	}
}

// TestSessionDeclaresDumbTerm asserts on sessionDeclaresDumbTerm itself:
// what serve adds is that its own process overrides whatever TERM it was
// started with.
func TestSessionDeclaresDumbTerm(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	sessionDeclaresDumbTerm()
	if got := os.Getenv("TERM"); got != "dumb" {
		t.Errorf("TERM = %q, want dumb", got)
	}
}
```

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./cmd/execd/`
Expected: FAIL to compile — `undefined: run`, `undefined: sessionDeclaresDumbTerm`.

- [ ] **Step 7: Write the binary**

Create `cmd/execd/main.go`. Copy the doc comments of `execdCmd`, `sessionDeclaresDumbTerm` and the long SIGTERM/in-flight comment inside `runExecd` from `internal/cli/execd.go` verbatim onto the matching code below (that file is deleted in Task 2; until then the two copies coexist).

```go
// Command execd serves shell command lines inside a sandbox session and sends
// them to it.
//
//	execd serve --socket <path>          serve until SIGTERM/SIGINT
//	execd exec [--timeout d] -- <cmd>    send one command line, exit with its status
//
// serve is not meant to run outside a sandbox: it executes whatever it is sent
// at its own grants. exec finds the socket through AGENT_SANDBOX_EXECD_SOCKET.
// There is no config file.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

const usage = `usage:
  execd serve --socket <path>
  execd exec [--timeout d] -- <command>`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "exec":
		return execCommand(args[1:])
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path to serve on (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "execd serve: --socket is required")
		return 2
	}

	sessionDeclaresDumbTerm()
	srv, err := execd.NewServer(*socket, execd.NewShellExecutor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "execd serve: %v\n", err)
		return 1
	}
	defer srv.Close()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		srv.Close()
	}()

	srv.Serve()
	return 0
}

func sessionDeclaresDumbTerm() {
	os.Setenv("TERM", "dumb")
}

func execCommand(args []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 0,
		"give up on the command after this duration, exiting 124 (0 = no limit)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	command := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(command) == "" {
		fmt.Fprintln(os.Stderr, "execd exec: no command given after --")
		return 2
	}
	return execd.RunCLI(context.Background(), command, *timeout,
		execd.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
}
```

- [ ] **Step 8: Run to verify they pass**

Run: `go test ./cmd/execd/ ./internal/execd/ ./internal/cli/ && go vet ./...`
Expected: PASS, no vet findings.

- [ ] **Step 9: Commit**

```bash
git add cmd/execd internal/execd/cli.go internal/execd/cli_test.go internal/execd/cli_internal_test.go internal/execd/protocol.go internal/cli/exec.go internal/cli/exec_test.go
git commit -m "feat(execd): add the standalone execd binary

What: cmd/execd serves (serve --socket) and sends (exec -- <cmd>) with the
stdlib flag package; the client loop moves to internal/execd.RunCLI and
agent-sandbox exec delegates to it.
Why: execd should be installable and reusable without agent-sandbox."
```

---

### Task 2: The launcher runs the `execd` binary

**Files:**
- Modify: `internal/claude/launch.go` (`ResolveExecd`, `BuildArgs`, `runDeps.startExecd`, `run`, `startExecd`, `ExecdArgs`)
- Modify: `internal/claude/launch_test.go`, `internal/claude/shellwrapper_test.go` (signatures, fake execd, new tests)
- Modify: `internal/claude/execd_e2e_test.go` (build `./cmd/execd`)
- Modify: `internal/cli/debug.go`
- Delete: `internal/cli/execd.go`, `internal/cli/execd_test.go`

**Interfaces:**
- Consumes: binary `execd serve --socket <path>` (Task 1).
- Produces: `func ResolveExecd() (string, error)` in package `claude` (used by Task 4).
- Produces: `func BuildArgs(cfg *config.Config, opts Options, profilePath, execdSocket, shellWrapper, execdPath string) (string, []string, error)` — empty `execdPath` adds no grant.
- Produces: `func ExecdArgs(cfg *config.Config, nonoPath, execdPath, sockPath, workdir string) []string` — entrypoint is `execdPath serve --socket sockPath`.
- Produces: `startExecd(cfg *config.Config) (socket, execdPath string, cleanup func(), err error)`; `runDeps.startExecd` has the same type.

- [ ] **Step 1: Write the failing tests**

In `internal/claude/launch_test.go`:

a. Make every fake nono directory also hold a fake `execd`, so `startExecd`'s lookup succeeds wherever a fake nono is on PATH. In `makeFakeNono` and `makeFakeNonoWedged`, after writing `nono`, add:

```go
	if err := os.WriteFile(filepath.Join(dir, "execd"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
```

b. Change `testExecdStart` to the new dependency type and give it a fixed execd path:

```go
const testExecdPath = "/opt/bin/execd"

func testExecdStart(sock string, cleaned *int) func(*config.Config) (string, string, func(), error) {
	return func(*config.Config) (string, string, func(), error) {
		return sock, testExecdPath, func() {
			if cleaned != nil {
				*cleaned++
			}
		}, nil
	}
}
```

The three inline `startExecd: func(*config.Config) (string, func(), error) {` literals (near lines 407, 1084, 1193) become `func(*config.Config) (string, string, func(), error) {` and their `return` statements gain a second value (`""` on error paths, `testExecdPath` otherwise).

c. Every `BuildArgs(...)` call in `launch_test.go` and `shellwrapper_test.go` gains a trailing `, ""` argument. Every `sock, cleanup, err := startExecd(&config.Config{})` becomes `sock, _, cleanup, err := startExecd(&config.Config{})`.

d. In the three `ExecdArgs` tests, replace the binary `"/opt/agent-sandbox/bin/agent-sandbox"` with `"/opt/bin/execd"` (rename the `self` const in `TestExecdArgsGrantsItsOwnBinaryAndInvokesItAbsolutely` to `execdPath`), and in that test add after the entrypoint check:

```go
	if got := args[dashIdx+2:]; strings.Join(got, " ") != "serve --socket /run/b.sock" {
		t.Errorf("after the entrypoint = %v, want serve --socket /run/b.sock", got)
	}
```

e. Add these tests:

```go
func TestBuildArgs_GrantsTheExecdBinary(t *testing.T) {
	makeFakeNono(t)
	_, args, err := BuildArgs(&config.Config{}, Options{}, "", "", "", "/opt/bin/execd")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}
	if !slices.Contains(argPairs(args, "--read-file"), "/opt/bin/execd") {
		t.Errorf("BuildArgs() = %v, want --read-file /opt/bin/execd so the agent can run execd exec", args)
	}
}

func TestResolveExecd_NotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveExecd()
	if err == nil || !strings.Contains(err.Error(), "go install github.com/ynny-github/agent-sandbox/cmd/execd@latest") {
		t.Fatalf("ResolveExecd() error = %v, want the install hint", err)
	}
}

func TestResolveExecd_ResolvesSymlinks(t *testing.T) {
	real := filepath.Join(t.TempDir(), "execd-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(real, filepath.Join(bin, "execd")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got, err := ResolveExecd()
	if err != nil {
		t.Fatalf("ResolveExecd() error = %v", err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got != want {
		t.Errorf("ResolveExecd() = %q, want the symlink target %q (Landlock checks the target)", got, want)
	}
}

func TestStartExecd_ExecdNotInPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nono"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, _, cleanup, err := startExecd(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "cmd/execd@latest") {
		t.Fatalf("startExecd() error = %v, want the execd install hint", err)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}
```

Add `slices` to the test file's imports if it is not already there. If `argPairs` (values following a flag) does not already exist in the test file, add it:

```go
// argPairs returns every value that directly follows flag in args.
func argPairs(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}
```

Add `TestRun_ExecdMissing_DoesNotLaunch`, modelled on the existing `TestRun_StartExecdFailure_DoesNotLaunch` (line ~397): copy that test, rename it, make its `startExecd` return `"", "", nil, fmt.Errorf("execd not found in PATH; install it with `go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`")`, and additionally assert the shell-wrapper cleanup ran — give it `startShellWrapper: func() (string, func(), error) { return "/tmp/w", func() { wrapperCleaned++ }, nil }` and require `wrapperCleaned == 1` and `superviseCalls == 0`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/claude/`
Expected: FAIL to compile — `undefined: ResolveExecd`, too many arguments to `BuildArgs`, `startExecd` assignment mismatch.

- [ ] **Step 3: Implement in `internal/claude/launch.go`**

Add beside `launcherPath`:

```go
// ResolveExecd finds the execd binary on PATH and resolves symlinks, because
// Landlock checks the target and a grant has to name it. The launcher calls it
// once per launch and feeds the same path to both sessions, so they cannot
// disagree about which execd they run.
func ResolveExecd() (string, error) {
	p, err := exec.LookPath("execd")
	if err != nil {
		return "", fmt.Errorf("execd not found in PATH; install it with "+
			"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		return resolved, nil
	}
	return p, nil
}
```

`BuildArgs` gains a final `execdPath string` parameter. Directly after the `args = append(args, "--read-file", self)` line add:

```go
	// The hook rewrites every command to `execd exec -- …`, which the agent
	// runs as its own child, so execd's binary needs the same read grant as
	// the launcher's own, for the same reason.
	if execdPath != "" {
		args = append(args, "--read-file", execdPath)
	}
```

`runDeps.startExecd` becomes `func(*config.Config) (socket, execdPath string, cleanup func(), err error)`.

In `run`:

```go
	execdSocket, execdPath, cleanupExecd, err := d.startExecd(cfg)
	...
	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, profilePath, execdSocket, shellWrapper, execdPath)
```

`startExecd`: signature `func startExecd(cfg *config.Config) (string, string, func(), error)`. Replace the `os.Executable()` block with:

```go
	execdPath, err := ResolveExecd()
	if err != nil {
		return "", "", nil, err
	}
```

Pass `execdPath` to `ExecdArgs` in place of `selfPath`; every `return "", nil, …` becomes `return "", "", nil, …`, and the success return becomes `return sockPath, execdPath, cleanup, nil`. Keep the nono lookup first, so `TestStartExecd_NonoNotInPath` still sees the nono error.

`ExecdArgs(cfg, nonoPath, execdPath, sockPath, workdir string)`: rename the parameter and change the tail to:

```go
		"--read-file", execdPath,
		"--allow-unix-socket-bind", sockPath,
		"--",
		execdPath, "serve", "--socket", sockPath,
```

Update `ExecdArgs`'s and `startExecd`'s doc comments where they say "agent-sandbox execd" or "its own binary" to name the execd binary.

- [ ] **Step 4: Update `internal/cli/debug.go`**

Replace the `os.Executable()` block and the `BuildArgs` call so debug prints what the launcher would build:

```go
	execdPath, execdErr := claude.ResolveExecd()
	grant := execdPath // "" when unresolved: the launch would refuse anyway

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, profilePath, execdSocket, shellWrapper, grant)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	if execdErr != nil {
		execdPath = "execd"
	}
	fmt.Fprintln(cmd.OutOrStdout(), "exec daemon:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join(
		claude.ExecdArgs(cfg, nonoPathForDisplay(), execdPath, execdSocket, cwd), " "))
	if execdErr != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "  ("+execdErr.Error()+")")
	}
```

- [ ] **Step 5: Delete the subcommand and fix e2e**

```bash
git rm internal/cli/execd.go internal/cli/execd_test.go
```

In `internal/claude/execd_e2e_test.go`, rename `buildAgentSandbox` to `buildExecd`, build `./cmd/execd` into `filepath.Join(dir, "execd")`, rename `selfPath` to `execdPath` in `startExecdSession` and its caller, and update the doc comment above `buildAgentSandbox` (it says the test builds agent-sandbox to run its `execd` subcommand).

- [ ] **Step 6: Run to verify they pass**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.
Then, with nono installed: `go test -tags e2e ./internal/claude/ -run Execd`
Expected: PASS (a real session serves via `execd serve`).

- [ ] **Step 7: Commit**

```bash
git add -A internal/claude internal/cli/debug.go internal/cli/execd.go internal/cli/execd_test.go
git commit -m "feat(claude): launch the execd binary found on PATH

What: The launcher resolves execd on PATH (symlinks resolved), runs
\`execd serve\` under the command profile and grants the same path to the
agent; agent-sandbox's hidden execd subcommand is removed.
Why: execd ships as its own binary; agent-sandbox only launches it."
```

---

### Task 3: The hook routes to `execd exec`

**Files:**
- Modify: `internal/cli/hook.go:77`
- Modify: `internal/cli/hook_test.go:48,60,111`
- Modify: `internal/claude/launch.go` (`hookProbeWant`)
- Delete: `internal/cli/exec.go`

**Interfaces:**
- Consumes: the agent-side `--read-file <execd>` grant (Task 2) and `execd exec` (Task 1).

- [ ] **Step 1: Update the tests to the new rewrite**

In `internal/cli/hook_test.go` replace each expected prefix:
- line 48: `` `execd exec -- 'git status'` ``
- line 60: `` `execd exec -- 'git commit -m '\''hi there'\'''` ``
- line 111: `"execd exec --"`

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run Hook`
Expected: FAIL — got `agent-sandbox exec -- …`.

- [ ] **Step 3: Implement**

`internal/cli/hook.go:77`:

```go
	wrapped := "execd exec -- " + shellquote.Quote(input.ToolInput.Command)
```

`internal/claude/launch.go`: `hookProbeWant    = "execd exec --"`.

```bash
git rm internal/cli/exec.go
```

Search for leftovers: `grep -rn "agent-sandbox exec\b" --include=*.go internal cmd` — update comments that describe the current routing (e.g. `internal/execd/client.go:194`, `internal/execd/job.go:148,184`, `internal/execd/protocol.go:261`, `internal/policysnapshot/policysnapshot.go:2`) to say `execd exec`. Leave strings in tests only where they assert old behaviour that no longer exists — there should be none.

- [ ] **Step 4: Run to verify they pass**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A internal
git commit -m "feat(hook): route agent commands through execd exec

What: The PreToolUse hook rewrites commands to \`execd exec -- <cmd>\` and
the launch probe expects that; agent-sandbox exec is removed.
Why: the client now ships in the execd binary."
```

---

### Task 4: doctor checks execd

**Files:**
- Modify: `internal/cli/doctor.go:417-519` (`selfPath`, `checkProfiles`)
- Modify: `internal/cli/doctor_test.go` (`stubSelfPath`, new tests)

**Interfaces:**
- Consumes: `claude.ResolveExecd() (string, error)` (Task 2).

- [ ] **Step 1: Write the failing tests**

In `internal/cli/doctor_test.go`, make the existing helper stub execd too, so every existing `checkProfiles` test keeps passing without execd installed:

```go
// stubSelfPath pins agent-sandbox's own path, and execd's beside it: both
// binaries are checked, and a test environment has neither on PATH.
func stubSelfPath(path string) func() {
	prevSelf, prevExecd := selfPath, execdPath
	selfPath = func() (string, error) { return path, nil }
	execdPath = func() (string, error) { return filepath.Join(filepath.Dir(path), "execd"), nil }
	return func() { selfPath, execdPath = prevSelf, prevExecd }
}
```

Add a fake that answers `nono why` per path:

```go
// nonoWritable answers like nonoAnswers(nil, "denied") except that `nono why`
// reports "allowed" for exactly the path writable.
func nonoWritable(writable string) func(context.Context, string, ...string) ([]byte, error) {
	base := nonoAnswers(nil, "denied")
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "why" && slices.Contains(args, writable) {
			return []byte(`{"status":"allowed"}`), nil
		}
		return base(ctx, name, args...)
	}
}
```

Add three tests, each set up like `TestCheckProfiles_FailsWhenNonoSaysTheBinaryIsWritable` (same `configWithBothProfiles`, `stubRunCommandEnv` returning `envProbeValue`, `stubSelfPath(filepath.Join(dir, "agent-sandbox"))`), differing only in the `stubRunCommand` fake and the assertion:

- `TestCheckProfiles_FailsWhenExecdIsWritable`: `stubRunCommand(nonoWritable(filepath.Join(dir, "execd")))`; want `ok == false` and `got.hint` containing `filepath.Join(dir, "execd")`.
- `TestCheckProfiles_FailsWhenAgentSandboxIsWritable`: `stubRunCommand(nonoWritable(filepath.Join(dir, "agent-sandbox")))`; want `ok == false` and `got.hint` containing `filepath.Join(dir, "agent-sandbox")`.
- `TestCheckProfiles_FailsWhenExecdIsNotOnPath`: `stubRunCommand(nonoAnswers(nil, "denied"))`, then override `execdPath = func() (string, error) { return "", errors.New("execd not found in PATH; install it with `go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`") }` (restore it with `defer`); want `ok == false` and `got.hint` containing `cmd/execd@latest`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run CheckProfiles`
Expected: FAIL to compile — `undefined: execdPath`.

- [ ] **Step 3: Implement**

In `internal/cli/doctor.go`, beside `selfPath`:

```go
// execdPath is claude.ResolveExecd, indirected so tests can place the execd
// binary anywhere without installing one.
var execdPath = claude.ResolveExecd
```

Change `selfPath`'s comment to say it locates agent-sandbox's own binary. In `checkProfiles`, replace everything from `self, err := selfPath()` to just before `r.ok = true` with:

```go
	self, err := selfPath()
	if err != nil {
		// Fail loudly rather than silently reporting OK: not knowing the
		// binary's path means the writability check below never ran, and
		// that must not look like it passed.
		r.details = append(r.details, fmt.Sprintf("error: could not determine agent-sandbox's own path: %v", err))
		r.hint = "could not verify agent-sandbox's binary is not writable through the command profile; " +
			"investigate why os.Executable() failed and re-run doctor"
		return r
	}
	execdBin, err := execdPath()
	if err != nil {
		r.details = append(r.details, "execd binary: "+err.Error())
		r.hint = "install execd: go install github.com/ynny-github/agent-sandbox/cmd/execd@latest"
		return r
	}
	r.details = append(r.details, "execd binary: "+execdBin)

	// Two files the command profile must not be able to write: a writable
	// execd replaces what the next session serves with, a writable
	// agent-sandbox replaces the launcher and the hook.
	for _, bin := range []string{execdBin, self} {
		writable, werr := profileAllowsWrite(ctx, cmdPath, bin)
		if werr != nil {
			r.details = append(r.details, fmt.Sprintf("error: could not ask nono whether %s is writable: %v", bin, werr))
			r.hint = "could not verify " + bin + " is not writable through the command profile; " +
				"fix the error above and re-run doctor"
			return r
		}
		if writable {
			r.hint = "the command profile grants write access to " + bin + ", so a command could " +
				"replace what the next launch runs; narrow the grant that covers it (`nono why --profile " +
				cmdPath + " --path " + bin + " --op write` names it)"
			return r
		}
	}
```

`TestCheckProfiles_FailsWhenSelfPathErrors` asserts on the old wording only through `ok`; if it also matches text, update it to the new "agent-sandbox's own path" wording.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/doctor.go internal/cli/doctor_test.go
git commit -m "feat(doctor): check the execd binary and both binaries' writability

What: doctor fails when execd is not on PATH, and when the command profile
can write either execd or agent-sandbox.
Why: they are two files now; checking one leaves the other replaceable."
```

---

### Task 5: Install path, docs, build task

**Files:**
- Modify: `README.md`, `README.ja.md`, `.mise.toml`, `agent-sandbox.toml:1`

No code; verification is a build and a grep.

- [ ] **Step 1: `.mise.toml` build task**

```toml
run = "go install -buildvcs=false ./cmd/..."
```

and its `description` to `"Build agent-sandbox and execd and install them outside the working tree (go install)"`.

- [ ] **Step 2: README.md**

- Diagram line 20: `└── nono run   --profile <command profile>  -- execd serve`
- Install block (line 38): `go install github.com/ynny-github/agent-sandbox/cmd/...@latest`, followed by one sentence: "This installs both `agent-sandbox` and `execd`; the launcher finds `execd` on `PATH`."
- mise block: add the line `"go:github.com/ynny-github/agent-sandbox/cmd/execd" = "latest"`.
- Line 114: `` the other runs `execd serve` under the command profile. ``
- Commands table: delete the `agent-sandbox exec` row; after the table add:

```markdown
`execd` is its own binary and takes no config file:

| Command | What it does |
|---|---|
| `execd serve --socket <path>` | Serve commands on the socket. Started by `agent-sandbox claude` inside the command-profile session; never run it unsandboxed, it executes whatever it is sent |
| `execd exec [--timeout d] -- <command>` | Send one command to the socket named by `AGENT_SANDBOX_EXECD_SOCKET` and stream its output |
```

- Doctor row: "…and that the command profile leaves neither `execd` nor `agent-sandbox` writable."
- Line 184: `` rewrites each command to `execd exec -- <command>`. ``; the next sentence: "…and `agent-sandbox` and `execd` must be on `PATH`."
- Development section: `mise run build        # install working-tree builds of both binaries via go install`.

- [ ] **Step 3: README.ja.md**

Mirror Step 2 at lines 20, 38, 115, 146, 180, 221 and the doctor row, in Japanese. The added table:

```markdown
`execd` は独立したバイナリで、設定ファイルを持ちません。

| コマンド | 内容 |
|---|---|
| `execd serve --socket <path>` | ソケットでコマンドを受け付ける。`agent-sandbox claude` がコマンドプロファイルのセッション内で起動する。受け取ったものをそのまま実行するので、sandbox の外では動かさないこと |
| `execd exec [--timeout d] -- <command>` | `AGENT_SANDBOX_EXECD_SOCKET` のソケットにコマンドを 1 つ送り、出力をストリームする |
```

- [ ] **Step 4: `agent-sandbox.toml:1`**

`` # The agent's Bash/Monitor commands are routed into `execd exec` by a ``

- [ ] **Step 5: Verify**

Run: `mise run build && command -v execd agent-sandbox && grep -rn "agent-sandbox exec\b\|agent-sandbox execd" README.md README.ja.md agent-sandbox.toml`
Expected: both binaries resolve to the same GOBIN directory; grep prints nothing.
Then run `agent-sandbox doctor` on the host. Expected: exit 0, with an `execd binary:` detail line.

- [ ] **Step 6: Commit**

```bash
git add README.md README.ja.md agent-sandbox.toml
git commit -m "docs: install and document execd as its own binary"
```

`.mise.toml` also carries an unrelated uncommitted change (`node`, `nono` tool pins) in the working tree at the time of writing. Do not stage it here: ask the operator whether those pins ship with the build-task change, then commit `.mise.toml` as they say (`build(mise): install every binary under cmd/`).

---

## Follow-ups (not in this plan)

- context-mode `src/exec-backend.ts` spawns `agent-sandbox exec`; it must switch to `execd exec` in the same release window, in that repository.
- execd's stderr messages are prefixed `agent-sandbox:` (`internal/execd/shell.go`); renaming them to `execd:` is a separate, visible change.
