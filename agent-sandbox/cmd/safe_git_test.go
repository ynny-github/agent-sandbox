package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunSafeGitCore_BlocksViolation(t *testing.T) {
	orig := execGit
	defer func() { execGit = orig }()
	called := false
	execGit = func(ctx context.Context, args []string) int { called = true; return 0 }

	var errb bytes.Buffer
	code := runSafeGitCore(context.Background(), []string{"push", "--force"}, &errb)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if called {
		t.Fatal("git must not run when a rule is violated")
	}
	if !strings.Contains(errb.String(), "blocked:") {
		t.Errorf("stderr = %q, want it to contain \"blocked:\"", errb.String())
	}
}

func TestRunSafeGitCore_PassesThroughWhenClean(t *testing.T) {
	orig := execGit
	defer func() { execGit = orig }()
	var gotArgs []string
	execGit = func(ctx context.Context, args []string) int { gotArgs = args; return 7 }

	code := runSafeGitCore(context.Background(), []string{"status", "-s"}, io.Discard)

	if code != 7 {
		t.Fatalf("exit code = %d, want 7 (git's own code propagated)", code)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "status" || gotArgs[1] != "-s" {
		t.Errorf("execGit args = %v, want [status -s]", gotArgs)
	}
}

func TestRunSafeGitCore_NoArgs(t *testing.T) {
	orig := execGit
	defer func() { execGit = orig }()
	execGit = func(ctx context.Context, args []string) int { t.Fatal("git must not run with no args"); return 0 }

	var errb bytes.Buffer
	if code := runSafeGitCore(context.Background(), nil, &errb); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
