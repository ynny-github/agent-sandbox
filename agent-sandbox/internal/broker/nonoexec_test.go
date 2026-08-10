package broker_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

func TestNonoExecutorArgs(t *testing.T) {
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/cmd-profile.json")
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
	path := filepath.Join(t.TempDir(), "stub-nono")
	script := `#!/bin/sh
# skip the 7 fixed args: run --silent --profile <p> --workdir <d> --
shift 7
echo "argv:$*"
echo "cwd:$PWD"
echo "stub-stderr" >&2
exit 3
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestNonoExecutorRunsProcess(t *testing.T) {
	stub := writeStubNono(t)
	workdir := t.TempDir()
	e := broker.NewNonoExecutor(stub, "/tmp/cmd-profile.json")

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
	e := broker.NewNonoExecutor("/usr/local/bin/nono", "/tmp/p.json")
	got := e.ProcessEnv(broker.Request{Env: []string{
		"AWS_PROFILE=dev",
		"NONO_ALLOW_DOMAIN=evil.example",
		"NONO_TRUST_OVERRIDE=1",
	}})
	for _, v := range got {
		if len(v) >= 5 && v[:5] == "NONO_" {
			t.Errorf("ProcessEnv() leaked %q into the nono process", v)
		}
	}
	if !slices.Contains(got, "AWS_PROFILE=dev") {
		t.Errorf("ProcessEnv() = %v, want it to contain AWS_PROFILE=dev", got)
	}
}
