package router

import (
	"bytes"
	"context"
	"io"
	"strings"
)

// Config holds routing patterns and the optional command runner.
type Config struct {
	AllowPatterns []string
	DropRules     []DropRule
	CommandRunner CommandRunner // nil allowed (host/drop-only lines)
}

// Router routes and runs command lines.
type Router struct {
	cfg Config
}

// New builds a Router from cfg.
func New(cfg Config) *Router { return &Router{cfg: cfg} }

// Result is the buffered outcome of RunBuffered.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run routes command and streams output to stdout/stderr, returning the exit
// code. The error is non-nil only on host-execution infrastructure failure.
func (s *Router) Run(ctx context.Context, command string, stdout, stderr io.Writer) (int, error) {
	return Run(ctx, Request{
		Command:       command,
		AllowPatterns: s.cfg.AllowPatterns,
		DropRules:     s.cfg.DropRules,
		CommandRunner: s.cfg.CommandRunner,
		Stdout:        stdout,
		Stderr:        stderr,
	})
}

// RunBuffered runs command and captures stdout/stderr into memory.
func (s *Router) RunBuffered(ctx context.Context, command string) (Result, error) {
	var out, errb bytes.Buffer
	code, err := s.Run(ctx, command, &out, &errb)
	return Result{Stdout: out.Bytes(), Stderr: errb.Bytes(), ExitCode: code}, err
}

// NeedsSandbox reports whether running command requires a command runner.
// It uses ParseLine to check each segment individually, so a pipeline with any
// sandbox-routed segment returns true, and a Fallback line always returns true.
func (s *Router) NeedsSandbox(command string) (bool, error) {
	line, err := ParseLine(command)
	if err != nil {
		return false, err
	}
	if line.Fallback {
		return true, nil
	}
	for _, pl := range line.Pipelines {
		for _, seg := range pl.Segments {
			if decision, _, _ := Route(strings.TrimSpace(seg.Raw), s.cfg.AllowPatterns, s.cfg.DropRules); decision == "sandbox" {
				return true, nil
			}
		}
	}
	return false, nil
}
