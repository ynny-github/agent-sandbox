package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Files holds the writers and host-side paths for one command's stdout and stderr output.
type Files struct {
	Stdout     io.Writer
	Stderr     io.Writer
	StdoutPath string
	StderrPath string
}

func (f *Files) Close() error {
	var errs []error
	if c, ok := f.Stdout.(io.Closer); ok {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if c, ok := f.Stderr.(io.Closer); ok {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func CreateFiles(dir string) (*Files, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("output: resolve dir: %w", err)
	}

	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, fmt.Errorf("output: create dir: %w", err)
	}

	for range 10 {
		files, err := createFiles(absDir)
		if err == nil {
			return files, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, err
	}

	return nil, fmt.Errorf("output: create files: too many filename collisions")
}

func createFiles(dir string) (*Files, error) {
	id := uuid.New().String()
	stdoutPath := filepath.Join(dir, id+".stdout")
	stderrPath := filepath.Join(dir, id+".stderr")

	stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, fmt.Errorf("output: create stdout file: %w", err)
	}

	stderrFile, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		stdoutFile.Close()
		os.Remove(stdoutPath)
		return nil, fmt.Errorf("output: create stderr file: %w", err)
	}

	return &Files{
		Stdout:     stdoutFile,
		Stderr:     stderrFile,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
	}, nil
}
