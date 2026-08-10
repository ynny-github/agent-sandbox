package broker_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

func TestNonoExecutorArgs(t *testing.T) {
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/cmd-profile.json", "/work/p", nil)
	got := e.Args(broker.Request{Argv: []string{"go", "build"}, Cwd: "/work/p"})

	want := []string{
		"run", "--silent",
		"--profile", "/tmp/cmd-profile.json",
		"--workdir", "/work/p",
		"--", "go", "build",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Args() = %v, want %v", got, want)
	}
}

// A stub standing in for nono: it echoes argv and exits with a chosen code, so
// the executor's process plumbing is tested without a real sandbox.
func writeStubNono(t *testing.T) string {
	t.Helper()
	return writeStub(t, `#!/bin/sh
# skip the 7 fixed args: run --silent --profile <p> --workdir <d> --
shift 7
echo "argv:$*"
echo "cwd:$PWD"
echo "stub-stderr" >&2
exit 3
`)
}

func writeStub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-nono")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestNonoExecutorRunsProcess(t *testing.T) {
	stub := writeStubNono(t)
	workdir := t.TempDir()
	e := broker.NewNonoExecutor(stub, "/tmp/cmd-profile.json", workdir, nil)

	var out, errb testBuffer
	code, err := e.Execute(context.Background(),
		broker.Request{Argv: []string{"hello"}, Cwd: workdir, Env: []string{"X=1"}},
		nil, &out, &errb)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !containsStr(out.String(), "argv:hello") {
		t.Errorf("stdout = %q, want it to contain argv:hello", out.String())
	}
	if !containsStr(errb.String(), "stub-stderr") {
		t.Errorf("stderr = %q, want it to contain stub-stderr", errb.String())
	}
}

func TestNonoExecutorDropsNonoEnvFromRequest(t *testing.T) {
	// NONO_* is rejected by config validation, so it can never be on the
	// allowlist; the strip stays as defence in depth.
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"AWS_PROFILE", "NONO_ALLOW_DOMAIN"})
	got := e.ProcessEnv(broker.Request{Env: []string{
		"AWS_PROFILE=dev",
		"NONO_ALLOW_DOMAIN=evil.example",
		"NONO_TRUST_OVERRIDE=1",
	}})
	for _, v := range got {
		if strings.HasPrefix(v, "NONO_") {
			t.Errorf("ProcessEnv() leaked %q into the nono process", v)
		}
	}
	if !slices.Contains(got, "AWS_PROFILE=dev") {
		t.Errorf("ProcessEnv() = %v, want it to contain AWS_PROFILE=dev", got)
	}
}

// The environment filter is an allowlist: a request variable the config does
// not permit must never reach the unsandboxed nono supervisor. XDG_STATE_HOME
// is the concrete abuse — it redirects nono's own audit ledger and session
// state out of ~/.local/state/nono.
func TestNonoExecutorProcessEnvDropsUnlistedRequestVars(t *testing.T) {
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"AWS_PROFILE"})
	got := e.ProcessEnv(broker.Request{Env: []string{
		"XDG_STATE_HOME=/tmp/hijack",
		"LD_PRELOAD=/tmp/evil.so",
		"AWS_PROFILE=dev",
	}})
	for _, v := range got {
		if strings.HasPrefix(v, "XDG_STATE_HOME=") || strings.HasPrefix(v, "LD_PRELOAD=") {
			t.Errorf("ProcessEnv() forwarded unlisted variable %q", v)
		}
	}
	if !slices.Contains(got, "AWS_PROFILE=dev") {
		t.Errorf("ProcessEnv() = %v, want the listed AWS_PROFILE=dev", got)
	}
}

// The baseline variables come from the launcher, never from the request.
func TestNonoExecutorProcessEnvUsesLauncherBaseline(t *testing.T) {
	t.Setenv("HOME", "/launcher/home")
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work", []string{"HOME"})
	got := e.ProcessEnv(broker.Request{Env: []string{"HOME=/attacker/home"}})

	if !slices.Contains(got, "HOME=/launcher/home") {
		t.Errorf("ProcessEnv() = %v, want HOME=/launcher/home", got)
	}
	if slices.Contains(got, "HOME=/attacker/home") {
		t.Errorf("ProcessEnv() let the request override the launcher's HOME: %v", got)
	}
}

func TestNonoExecutorRejectsCwdOutsideGrantedDir(t *testing.T) {
	granted := t.TempDir()
	outside := t.TempDir()
	e := broker.NewNonoExecutor(writeStubNono(t), "/tmp/p.json", granted, nil)

	var out, errb testBuffer
	_, err := e.Execute(context.Background(),
		broker.Request{Argv: []string{"hello"}, Cwd: outside}, nil, &out, &errb)
	if err == nil {
		t.Fatal("Execute() accepted a working directory outside the granted directory")
	}
	if !containsStr(err.Error(), "outside the sandboxed directory") {
		t.Errorf("Execute() error = %v, want it to mention the granted directory", err)
	}
	if out.String() != "" {
		t.Errorf("Execute() ran the command anyway; stdout = %q", out.String())
	}
}

func TestNonoExecutorRejectsRelativeCwd(t *testing.T) {
	e := broker.NewNonoExecutor(writeStubNono(t), "/tmp/p.json", t.TempDir(), nil)
	var out, errb testBuffer
	_, err := e.Execute(context.Background(),
		broker.Request{Argv: []string{"hello"}, Cwd: "relative/dir"}, nil, &out, &errb)
	if err == nil {
		t.Fatal("Execute() accepted a relative working directory")
	}
	if !containsStr(err.Error(), "absolute") {
		t.Errorf("Execute() error = %v, want it to mention an absolute path", err)
	}
}

func TestNonoExecutorAcceptsCwdBelowGrantedDir(t *testing.T) {
	granted := t.TempDir()
	sub := filepath.Join(granted, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	e := broker.NewNonoExecutor(writeStubNono(t), "/tmp/p.json", granted, nil)

	var out, errb testBuffer
	code, err := e.Execute(context.Background(),
		broker.Request{Argv: []string{"hello"}, Cwd: sub}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

// A signal death must report the conventional 128+signum a shell user expects,
// not ExitCode()'s -1 (which the CLI turns into status 255).
func TestNonoExecutorReportsSignalDeathAs128PlusSignal(t *testing.T) {
	workdir := t.TempDir()
	stub := writeStub(t, "#!/bin/sh\nkill -9 $$\n")
	e := broker.NewNonoExecutor(stub, "/tmp/p.json", workdir, nil)

	var out, errb testBuffer
	code, err := e.Execute(context.Background(),
		broker.Request{Argv: []string{"boom"}, Cwd: workdir}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if code != 137 {
		t.Errorf("exit code = %d, want 137 (128 + SIGKILL)", code)
	}
}
