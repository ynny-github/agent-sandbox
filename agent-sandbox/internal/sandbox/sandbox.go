package sandbox

import (
	"bytes"
	"context"
	"io"
)

// Config holds routing patterns and the optional container runner.
type Config struct {
	AllowPatterns           []string
	DropPatterns            []string
	ContainerEnvPassthrough []string
	ContainerRunner         ContainerRunner // nil allowed (host/drop-only lines)
}

// Sandbox routes and runs command lines.
type Sandbox struct {
	cfg Config
}

// New builds a Sandbox from cfg.
func New(cfg Config) *Sandbox { return &Sandbox{cfg: cfg} }

// Result is the buffered outcome of RunBuffered.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run routes command and streams output to stdout/stderr, returning the exit
// code. The error is non-nil only on host-execution infrastructure failure.
func (s *Sandbox) Run(ctx context.Context, command string, stdout, stderr io.Writer) (int, error) {
	return Run(ctx, Request{
		Command:                 command,
		AllowPatterns:           s.cfg.AllowPatterns,
		DropPatterns:            s.cfg.DropPatterns,
		ContainerRunner:         s.cfg.ContainerRunner,
		ContainerEnvPassthrough: s.cfg.ContainerEnvPassthrough,
		Stdout:                  stdout,
		Stderr:                  stderr,
	})
}

// RunBuffered runs command and captures stdout/stderr into memory.
func (s *Sandbox) RunBuffered(ctx context.Context, command string) (Result, error) {
	var out, errb bytes.Buffer
	code, err := s.Run(ctx, command, &out, &errb)
	return Result{Stdout: out.Bytes(), Stderr: errb.Bytes(), ExitCode: code}, err
}

// NeedsContainer reports whether running command requires a container runner.
func (s *Sandbox) NeedsContainer(command string) (bool, error) {
	decision, _ := Route(command, s.cfg.AllowPatterns, s.cfg.DropPatterns)
	return decision == "container", nil
}
