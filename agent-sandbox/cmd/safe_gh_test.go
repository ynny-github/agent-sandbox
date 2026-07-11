package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunSafeGhCore_BlocksViolation(t *testing.T) {
	orig := execGh
	defer func() { execGh = orig }()
	called := false
	execGh = func(ctx context.Context, args []string) int { called = true; return 0 }

	var errb bytes.Buffer
	code := runSafeGhCore(context.Background(), []string{"pr", "merge", "5"}, &errb)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if called {
		t.Fatal("gh must not run when a rule is violated")
	}
	if !strings.Contains(errb.String(), "blocked:") {
		t.Errorf("stderr = %q, want it to contain \"blocked:\"", errb.String())
	}
}

func TestRunSafeGhCore_BlocksApi(t *testing.T) {
	orig := execGh
	defer func() { execGh = orig }()
	execGh = func(ctx context.Context, args []string) int { t.Fatal("gh api must not run"); return 0 }

	var errb bytes.Buffer
	if code := runSafeGhCore(context.Background(), []string{"api", "/repos/o/r"}, &errb); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestRunSafeGhCore_PassesThroughWhenClean(t *testing.T) {
	orig := execGh
	defer func() { execGh = orig }()
	var gotArgs []string
	execGh = func(ctx context.Context, args []string) int { gotArgs = args; return 7 }

	code := runSafeGhCore(context.Background(), []string{"pr", "list"}, io.Discard)

	if code != 7 {
		t.Fatalf("exit code = %d, want 7 (gh's own code propagated)", code)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "pr" || gotArgs[1] != "list" {
		t.Errorf("execGh args = %v, want [pr list]", gotArgs)
	}
}

func TestRunSafeGhCore_NoArgs(t *testing.T) {
	orig := execGh
	defer func() { execGh = orig }()
	execGh = func(ctx context.Context, args []string) int { t.Fatal("gh must not run with no args"); return 0 }

	var errb bytes.Buffer
	if code := runSafeGhCore(context.Background(), nil, &errb); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
