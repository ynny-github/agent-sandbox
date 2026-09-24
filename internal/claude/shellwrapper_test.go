package claude

import (
	"errors"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// socketStdin returns a connected socket pair's two ends. Claude Code spawns
// the agent's shell with a socket on stdin (measured), and that is the whole
// reason this wrapper exists: bash treats a socket on stdin as "I was started
// by rshd/sshd" and sources ~/.bashrc even when it is not interactive.
func socketStdin(t *testing.T) (child, parent *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	child = os.NewFile(uintptr(fds[0]), "sock-child")
	parent = os.NewFile(uintptr(fds[1]), "sock-parent")
	t.Cleanup(func() { child.Close(); parent.Close() })
	return child, parent
}

// homeWithBashrc returns a HOME whose .bashrc announces itself on stderr.
func homeWithBashrc(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	rc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(rc, []byte("echo RC-SOURCED >&2\n"), 0o600); err != nil {
		t.Fatalf("write .bashrc: %v", err)
	}
	return home
}

// runWithSocketStdin runs name with a socket on stdin and returns its combined
// output.
func runWithSocketStdin(t *testing.T, home, name string, args ...string) string {
	t.Helper()
	child, _ := socketStdin(t)
	cmd := exec.Command(name, args...)
	cmd.Stdin = child
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return string(out)
}

// bashForTest locates the bash the wrapper delegates to, and proves this host
// actually reproduces the hazard. If bare bash does NOT source ~/.bashrc from a
// socket stdin here, the wrapper test below would pass for the wrong reason.
func bashForTest(t *testing.T) string {
	t.Helper()
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	out := runWithSocketStdin(t, homeWithBashrc(t), bashPath, "-c", "echo RAN")
	if !strings.Contains(out, "RC-SOURCED") {
		t.Skipf("this host's bash does not source ~/.bashrc from a socket stdin; "+
			"the wrapper has nothing to prove here (got %q)", out)
	}
	return bashPath
}

func TestShellWrapper_DoesNotSourceBashrcWhenStdinIsASocket(t *testing.T) {
	bashPath := bashForTest(t)
	wrapper := filepath.Join(t.TempDir(), "norc-bash")
	if err := writeShellWrapper(wrapper, bashPath); err != nil {
		t.Fatalf("writeShellWrapper() error = %v", err)
	}

	out := runWithSocketStdin(t, homeWithBashrc(t), wrapper, "-c", "echo RAN")

	if strings.Contains(out, "RC-SOURCED") {
		t.Errorf("wrapper sourced ~/.bashrc; output = %q", out)
	}
	if !strings.Contains(out, "RAN") {
		t.Errorf("wrapper did not run the command; output = %q", out)
	}
}

func TestShellWrapperPath_CarriesBashInItsName(t *testing.T) {
	// Claude ignores CLAUDE_CODE_SHELL unless the path string contains "bash"
	// or "zsh", so the file name is load-bearing, not cosmetic.
	path, err := ShellWrapperPath()
	if err != nil {
		t.Fatalf("ShellWrapperPath() error = %v", err)
	}
	if !strings.Contains(filepath.Base(path), "bash") {
		t.Errorf("ShellWrapperPath() = %q; Claude ignores a path without \"bash\" in it", path)
	}
}

func TestBuildArgs_GrantsTheShellWrapperToTheAgentSandbox(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}

	_, args, err := BuildArgs(cfg, Options{}, "", "", "/state/norc-bash-1")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}

	// Without the read grant nono refuses the execve and Claude falls back to
	// the host's bash, which is the noise this wrapper exists to remove.
	if !hasFlagValue(args, "--read-file", "/state/norc-bash-1") {
		t.Errorf("BuildArgs() = %v; want --read-file /state/norc-bash-1", args)
	}
}

func TestBuildArgs_OmitsTheShellWrapperGrantWhenThereIsNoWrapper(t *testing.T) {
	makeFakeNono(t)
	cfg := &config.Config{}

	_, args, err := BuildArgs(cfg, Options{}, "", "", "")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v", err)
	}

	for i, a := range args {
		if a == "--read-file" && i+1 < len(args) && strings.Contains(args[i+1], "norc-bash") {
			t.Errorf("BuildArgs() granted a wrapper that was never created: %v", args)
		}
	}
}

// testWrapperStart is the shell-wrapper counterpart of testExecdStart.
func testWrapperStart(path string, cleaned *int) func() (string, func(), error) {
	return func() (string, func(), error) {
		return path, func() {
			if cleaned != nil {
				*cleaned++
			}
		}, nil
	}
}

func TestRun_ExportsTheShellWrapperBeforeSupervise(t *testing.T) {
	makeFakeNono(t)
	t.Setenv(ShellEnvVar, "")
	const wantWrapper = "/tmp/test-norc-bash-1"
	var gotEnv string
	var gotArgs []string
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile:      func(*config.Config) (string, error) { return "/tmp/asb-profile-1.json", nil },
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart(wantWrapper, nil),
		supervise: func(_ string, args []string) int {
			gotEnv = os.Getenv(ShellEnvVar)
			gotArgs = args
			return 0
		},
		exit: func(int) {},
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if gotEnv != wantWrapper {
		t.Errorf("%s = %q at supervise time, want %q", ShellEnvVar, gotEnv, wantWrapper)
	}
	if !hasFlagValue(gotArgs, "--read-file", wantWrapper) {
		t.Errorf("run() argv = %v; want --read-file %s", gotArgs, wantWrapper)
	}
}

func TestRun_CleansUpTheShellWrapperBeforeExit(t *testing.T) {
	makeFakeNono(t)
	cleaned := 0
	cleanedBeforeExit := false
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile:      func(*config.Config) (string, error) { return "/tmp/asb-profile-1.json", nil },
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: testWrapperStart("/tmp/test-norc-bash-1", &cleaned),
		supervise:         func(string, []string) int { return 0 },
		exit:              func(int) { cleanedBeforeExit = cleaned == 1 },
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !cleanedBeforeExit {
		t.Error("the wrapper must be removed before exit; os.Exit skips deferred cleanup in production")
	}
}

func TestRun_LaunchesWithoutTheWrapperWhenItCannotBeWritten(t *testing.T) {
	makeFakeNono(t)
	t.Setenv(ShellEnvVar, "")
	superviseCalls := 0
	// A missing wrapper costs noise, not safety: Claude falls back to the
	// host's bash and every tool result carries a line about ~/.bashrc. That
	// is not worth refusing to launch over.
	err := run(&config.Config{}, Options{}, runDeps{
		agentProfile:      func(*config.Config) (string, error) { return "/tmp/asb-profile-1.json", nil },
		verifyHook:        func(string, string) error { return nil },
		startExecd:        testExecdStart("/tmp/test.sock", nil),
		startShellWrapper: func() (string, func(), error) { return "", nil, errors.New("no state dir") },
		supervise:         func(string, []string) int { superviseCalls++; return 0 },
		exit:              func(int) {},
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if superviseCalls != 1 {
		t.Fatalf("supervise called %d times, want 1", superviseCalls)
	}
	if os.Getenv(ShellEnvVar) != "" {
		t.Errorf("%s = %q; a wrapper that was never written must not be named",
			ShellEnvVar, os.Getenv(ShellEnvVar))
	}
}
