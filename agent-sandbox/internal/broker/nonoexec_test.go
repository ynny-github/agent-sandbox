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
		broker.Request{Argv: []string{"hello"}, Cwd: workdir},
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

func TestNonoExecutorDropsNonoEnvFromAllowlist(t *testing.T) {
	// NONO_* is rejected by config validation, so it can never legitimately be
	// on the allowlist; the strip stays as defence in depth.
	t.Setenv("AWS_PROFILE", "dev")
	t.Setenv("NONO_ALLOW_DOMAIN", "evil.example")
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"AWS_PROFILE", "NONO_ALLOW_DOMAIN"})

	got := e.ProcessEnv()
	for _, v := range got {
		if strings.HasPrefix(v, "NONO_") {
			t.Errorf("ProcessEnv() leaked %q into the nono process", v)
		}
	}
	if !slices.Contains(got, "AWS_PROFILE=dev") {
		t.Errorf("ProcessEnv() = %v, want it to contain AWS_PROFILE=dev", got)
	}
}

// The environment is an allowlist over the LAUNCHER's variables: anything the
// profile does not declare must never reach the unsandboxed nono supervisor.
// XDG_STATE_HOME is the concrete abuse — it redirects nono's own audit ledger
// and session state out of ~/.local/state/nono.
func TestNonoExecutorProcessEnvDropsUnlistedVars(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/hijack")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("AWS_PROFILE", "dev")
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"AWS_PROFILE"})

	got := e.ProcessEnv()
	for _, v := range got {
		if strings.HasPrefix(v, "XDG_STATE_HOME=") || strings.HasPrefix(v, "LD_PRELOAD=") {
			t.Errorf("ProcessEnv() forwarded unlisted variable %q", v)
		}
	}
	if !slices.Contains(got, "AWS_PROFILE=dev") {
		t.Errorf("ProcessEnv() = %v, want the listed AWS_PROFILE=dev", got)
	}
}

// allow_vars entries are patterns. A trailing "*" is a prefix match — that is
// how the mise capability's "MISE*" / "__MISE*" grant is satisfied.
func TestNonoExecutorProcessEnvMatchesTrailingStarPrefix(t *testing.T) {
	t.Setenv("MISE_SHELL", "fish")
	t.Setenv("__MISE_SESSION", "1")
	t.Setenv("MISCELLANEOUS", "no")
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"MISE*", "__MISE*"})

	got := e.ProcessEnv()
	for _, want := range []string{"MISE_SHELL=fish", "__MISE_SESSION=1"} {
		if !slices.Contains(got, want) {
			t.Errorf("ProcessEnv() = %v, want it to contain %q", got, want)
		}
	}
	if slices.Contains(got, "MISCELLANEOUS=no") {
		t.Errorf("ProcessEnv() matched a variable outside the MISE* prefix: %v", got)
	}
}

// Unsupported pattern shapes fail closed rather than over-granting. A bare "*"
// must not turn into "forward the launcher's whole environment".
func TestNonoExecutorProcessEnvRejectsUnsupportedPatterns(t *testing.T) {
	t.Setenv("SOME_SECRET", "s3cret")
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json", "/work",
		[]string{"*", "SOME_*_SECRET", "*SECRET"})

	if got := e.ProcessEnv(); len(got) != 0 {
		t.Errorf("ProcessEnv() = %v, want an empty environment for unsupported patterns", got)
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
