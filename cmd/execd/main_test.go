package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
func TestUp_RequiresProfile(t *testing.T) {
	if code := run([]string{"up"}); code != 2 {
		t.Errorf("run(up) = %d, want 2 without --profile", code)
	}
}

func TestDown_TakesAtMostOneSocket(t *testing.T) {
	for _, args := range [][]string{{"down", "a", "b"}} {
		if code := run(args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}

func TestDown_RefusalExits1(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code := run([]string{"down", "/tmp/not-a-session/execd.sock"}); code != 1 {
		t.Errorf("run(down outside root) = %d, want 1", code)
	}
}

func TestGC_BadIdleIsUsage(t *testing.T) {
	if code := run([]string{"gc", "--idle", "soon"}); code != 2 {
		t.Errorf("run(gc --idle soon) = %d, want 2", code)
	}
}

func TestGC_EmptyRootSucceeds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code := run([]string{"gc", "--dry-run"}); code != 0 {
		t.Errorf("run(gc --dry-run) on an empty root = %d, want 0", code)
	}
}

func TestDown_WithoutASocketSucceedsWhenNothingIsRunning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code := run([]string{"down"}); code != 0 {
		t.Errorf("run(down) = %d, want 0", code)
	}
}

// Claude Code blocks a tool call only on exit 2. Every other non-zero exit is a
// non-blocking error, after which the ORIGINAL command runs — unwrapped, in the
// agent's own sandbox. So every failure of `execd hook claude` has to be a 2,
// and so does an unknown agent name.
func TestHook_FailsClosedWithExitCode2(t *testing.T) {
	for name, payload := range map[string]string{
		"unparseable": `not json at all`,
		"no command":  `{"tool_name":"Bash","tool_input":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runHook("claude", strings.NewReader(payload), &out, &errOut); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if errOut.Len() == 0 {
				t.Error("a refusal must say why on stderr; Claude Code feeds it back to the agent")
			}
		})
	}
}

func TestHook_ClaudeSucceedsWithExitCode0(t *testing.T) {
	var out, errOut bytes.Buffer
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	if code := runHook("claude", in, &out, &errOut); code != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "execd exec --") {
		t.Errorf("expected the rewritten command; got %q", out.String())
	}
}

func TestHook_UnknownOrMissingAgentExits2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runHook("codex", strings.NewReader("{}"), &out, &errOut); code != 2 {
		t.Errorf("runHook(codex) = %d, want 2", code)
	}
	if code := run([]string{"hook"}); code != 2 {
		t.Errorf("run(hook) = %d, want 2", code)
	}
}
