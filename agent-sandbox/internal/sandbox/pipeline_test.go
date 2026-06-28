package sandbox_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandbox"
)

// echoRunner copies its stdin to stdout (models `cat` in the container).
type echoRunner struct{ gotArgv [][]string }

func (e *echoRunner) RunContainer(_ context.Context, argv, _ []string, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	e.gotArgv = append(e.gotArgv, argv)
	if stdin != nil {
		io.Copy(stdout, stdin)
	}
	return 0, nil
}

func TestRun_MixedPipe_HostToContainer(t *testing.T) {
	// `echo hi` on host (allowed) | `cat` in container.
	r := &echoRunner{}
	var out, errb bytes.Buffer
	code, err := sandbox.Run(context.Background(), sandbox.Request{
		Command:         "echo hi | cat",
		AllowPatterns:   []string{"echo *"}, // echo→host, cat→container
		ContainerRunner: r,
		Stdout:          &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("err = %v stderr=%q", err, errb.String())
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if out.String() != "hi\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "hi\n")
	}
	// Container segment ran as argv (cat), not bash -c the whole pipeline.
	if len(r.gotArgv) != 1 || r.gotArgv[0][0] == "bash" {
		t.Fatalf("container argv = %#v, want a single non-bash cat call", r.gotArgv)
	}
}
