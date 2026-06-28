package executor_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
)

var errFailingWriter = errors.New("failing writer")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errFailingWriter
}

func TestRunHost_Echo_WritesStdoutAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := executor.RunHost(context.Background(), []string{"echo", "hello"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestRunHost_BothOutputs_WrittenToCorrectWriters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Use printf to write to stdout and redirect to stderr via fd-to-fd (permitted by validator)
	code, err := executor.RunHost(context.Background(),
		[]string{"sh", "-c", "printf out && printf err 1>&2"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err")
	}
}

func TestRunHost_NonZeroExit_ReturnsExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := executor.RunHost(context.Background(), []string{"false"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestRunHost_OutputBeforeExit_IsPreserved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Write output then exit with code 1
	code, err := executor.RunHost(context.Background(),
		[]string{"sh", "-c", "echo partial; exit 1"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout.String() != "partial\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "partial\n")
	}
}

func TestRunHost_NoOutput_ExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := executor.RunHost(context.Background(), []string{"true"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestRunHost_ContextCancelled_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before start
	var stdout, stderr bytes.Buffer
	_, err := executor.RunHost(ctx, []string{"sleep", "10"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestRunHost_DeadlineKillsProcessGroupPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer

	start := time.Now()
	code, err := executor.RunHost(ctx, []string{"sh", "-c", "sleep 10 &"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RunHost took %v, want process group killed promptly", elapsed)
	}
}

func TestRunHost_DeadlineExceeded_ReturnsExitCode124(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer

	code, err := executor.RunHost(ctx, []string{"sleep", "60"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
}

func TestRunHost_DeadlineExceeded_PreservesPartialOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer

	code, err := executor.RunHost(ctx, []string{"sh", "-c", "echo partial; sleep 60"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
	if stdout.String() != "partial\n" {
		t.Errorf("stdout = %q, want %q (partial output should be preserved)", stdout.String(), "partial\n")
	}
}

func TestRunHost_DeadlineExceeded_CopyErrorReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var stderr bytes.Buffer

	code, err := executor.RunHost(ctx, []string{"sh", "-c", "yes; sleep 60"}, failingWriter{}, &stderr)
	if err == nil {
		t.Fatal("expected copy error, got nil")
	}
	if !errors.Is(err, errFailingWriter) {
		t.Fatalf("error = %v, want failing writer error", err)
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
}

func TestRunHost_CopyError_ReturnsError(t *testing.T) {
	var stderr bytes.Buffer
	code, err := executor.RunHost(context.Background(), []string{"printf", "hello"}, failingWriter{}, &stderr)
	if err == nil {
		t.Fatal("expected copy error, got nil")
	}
	if !errors.Is(err, errFailingWriter) {
		t.Fatalf("error = %v, want failing writer error", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunHost_StderrCopyError_ReturnsError(t *testing.T) {
	var stdout bytes.Buffer
	code, err := executor.RunHost(context.Background(),
		[]string{"sh", "-c", "printf err 1>&2"}, &stdout, failingWriter{})
	if err == nil {
		t.Fatal("expected copy error, got nil")
	}
	if !errors.Is(err, errFailingWriter) {
		t.Fatalf("error = %v, want failing writer error", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunHost_EmptyArgs_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if _, err := executor.RunHost(context.Background(), nil, &stdout, &stderr); err == nil {
		t.Fatal("expected error for empty args, got nil")
	}
}

var _ io.Writer = failingWriter{}
