package output_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
)

func TestCreateFiles_ReturnsDistinctPaths(t *testing.T) {
	dir := t.TempDir()
	files, err := output.CreateFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		closeFiles(t, files)
	})
	if files.StdoutPath == files.StderrPath {
		t.Error("StdoutPath and StderrPath must be distinct")
	}
	if files.Stdout == nil || files.Stderr == nil {
		t.Error("writers must not be nil")
	}
}

func TestCreateFiles_CreatesDirectoryIfAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "output")
	files, err := output.CreateFiles(dir)
	if err != nil {
		t.Fatalf("expected directory to be created: %v", err)
	}
	t.Cleanup(func() {
		closeFiles(t, files)
	})
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("directory should exist after CreateFiles")
	}
}

func TestCreateFiles_ReturnsAbsolutePathsForRelativeDir(t *testing.T) {
	relativeDir := filepath.Join("test-output", "relative")
	t.Cleanup(func() {
		if err := os.RemoveAll("test-output"); err != nil {
			t.Errorf("remove test output: %v", err)
		}
	})

	files, err := output.CreateFiles(relativeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		closeFiles(t, files)
	})

	if !filepath.IsAbs(files.StdoutPath) {
		t.Errorf("StdoutPath should be absolute, got %q", files.StdoutPath)
	}
	if !filepath.IsAbs(files.StderrPath) {
		t.Errorf("StderrPath should be absolute, got %q", files.StderrPath)
	}
}

func TestCreateFiles_WrittenDataAppearsInStdout(t *testing.T) {
	dir := t.TempDir()
	files, err := output.CreateFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		closeFiles(t, files)
	})
	if _, err := files.Stdout.Write([]byte("hello stdout")); err != nil {
		t.Fatalf("write error: %v", err)
	}
	closeWriter(t, files.Stdout)
	data, err := os.ReadFile(files.StdoutPath)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if string(data) != "hello stdout" {
		t.Errorf("got %q, want %q", string(data), "hello stdout")
	}
}

func TestCreateFiles_WrittenDataAppearsInStderr(t *testing.T) {
	dir := t.TempDir()
	files, err := output.CreateFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		closeFiles(t, files)
	})
	if _, err := files.Stderr.Write([]byte("hello stderr")); err != nil {
		t.Fatalf("write error: %v", err)
	}
	closeWriter(t, files.Stderr)
	data, err := os.ReadFile(files.StderrPath)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if string(data) != "hello stderr" {
		t.Errorf("got %q, want %q", string(data), "hello stderr")
	}
}

func TestCreateFiles_ConcurrentCallsProduceUniqueNames(t *testing.T) {
	dir := t.TempDir()
	const n = 10
	paths := make(chan string, n*2)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := output.CreateFiles(dir)
			if err != nil {
				t.Errorf("CreateFiles error: %v", err)
				return
			}
			defer closeFiles(t, f)
			paths <- f.StdoutPath
			paths <- f.StderrPath
		}()
	}
	wg.Wait()
	close(paths)

	seen := make(map[string]bool)
	for p := range paths {
		if seen[p] {
			t.Errorf("duplicate path: %s", p)
		}
		seen[p] = true
	}
}

type closableWriter interface {
	Close() error
}

func closeFiles(t *testing.T, files *output.Files) {
	t.Helper()
	closeWriter(t, files.Stdout)
	closeWriter(t, files.Stderr)
}

func closeWriter(t *testing.T, writer interface{}) {
	t.Helper()
	closer, ok := writer.(closableWriter)
	if !ok {
		t.Fatalf("writer does not implement Close")
	}
	if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close writer: %v", err)
	}
}
