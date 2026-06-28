// Package sandbox routes a command to drop/host/container and executes it,
// independent of any transport (MCP, CLI). Output is written to the caller's
// io.Writers.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ContainerRunner executes an argv inside the sandbox container.
type ContainerRunner interface {
	RunContainer(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

// Request carries everything Run needs for a single command.
type Request struct {
	Command                 string
	AllowPatterns           []string
	DropPatterns            []string
	ContainerRunner         ContainerRunner
	ContainerEnvPassthrough []string
	Stdout                  io.Writer
	Stderr                  io.Writer
}

// routedSeg pairs a parsed Segment with its routing decision.
type routedSeg struct {
	seg      Segment
	decision string
}

// Run routes req.Command per segment and executes the resulting pipelines in
// sequence, honoring && / || / ; operators between pipelines.
func Run(ctx context.Context, req Request) (int, error) {
	line, parseErr := ParseLine(req.Command)
	if parseErr != nil {
		fmt.Fprintf(req.Stderr, "rejected: %v\n", parseErr)
		return 1, nil
	}

	// Fallback: $(), backtick, or lone & — must run whole line in a shell.
	if line.Fallback {
		return runContainerWhole(ctx, req, req.Command)
	}

	// Route every segment up front. Two-pass: first check all segments for drop
	// (fail-closed before any execution), then check for missing container runner.
	// Trim each segment's Raw before routing to strip structural whitespace left
	// by the pipeline/sequential split (e.g. " b" or "false ").
	// INVARIANT: trimming is for pattern matching only; execution always uses the
	// original seg.Raw / seg.Args so routing semantics are never altered by whitespace.
	plDecisions := make([][]routedSeg, len(line.Pipelines))
	for i, pl := range line.Pipelines {
		for _, seg := range pl.Segments {
			decision, matched := Route(strings.TrimSpace(seg.Raw), req.AllowPatterns, req.DropPatterns)
			if decision == "drop" {
				fmt.Fprintf(req.Stderr, "dropped: command matches drop pattern %q\n", matched)
				return 1, nil
			}
			plDecisions[i] = append(plDecisions[i], routedSeg{seg: seg, decision: decision})
		}
	}
	// Second pass: check for missing container runner now that drops are clear.
	for _, pl := range plDecisions {
		for _, r := range pl {
			if r.decision == "container" && req.ContainerRunner == nil {
				fmt.Fprintln(req.Stderr, "no container configured: cannot route command to container")
				return 1, nil
			}
		}
	}

	// Execute pipelines in order, honoring sequential operators.
	lastExit := 0
	for i, pl := range line.Pipelines {
		if i > 0 {
			switch line.Seps[i-1] {
			case "&&":
				if lastExit != 0 {
					continue // skip on failure
				}
			case "||":
				if lastExit == 0 {
					continue // skip on success
				}
			}
			// ";" always runs
		}
		code, err := runPipeline(ctx, req, pl, plDecisions[i])
		if err != nil {
			return code, err
		}
		lastExit = code
	}
	return lastExit, nil
}

// runPipeline runs one pipeline. Uniform pipelines (all segments on the same
// side) run as a single invocation on that side. Mixed host+container pipelines
// are wired segment-by-segment with io.Pipe via runMixedPipeline.
func runPipeline(ctx context.Context, req Request, pl PipelineNode, rs []routedSeg) (int, error) {
	allHost := true
	allContainer := true
	for _, r := range rs {
		if r.decision == "container" {
			allHost = false
		} else {
			allContainer = false
		}
	}

	switch {
	case allHost:
		return runUniformHost(ctx, req, pl, rs)
	case allContainer:
		return runUniformContainer(ctx, req, pl, rs)
	default:
		return runMixedPipeline(ctx, req, rs)
	}
}

// runUniformHost runs a pipeline where every segment routes to the host.
// A single simple segment (no redirect) runs as shell-free argv, preserving
// the existing behavior from before Task 6. Multiple segments or a redirect
// run via bash -c on the pipeline raw.
func runUniformHost(ctx context.Context, req Request, pl PipelineNode, rs []routedSeg) (int, error) {
	// Single simple segment → shell-free argv (no bash -c).
	if len(rs) == 1 && !rs[0].seg.HasRedirect {
		if len(rs[0].seg.Args) == 0 {
			fmt.Fprintln(req.Stderr, "rejected: empty command")
			return 1, nil
		}
		code, err := RunHost(ctx, rs[0].seg.Args, nil, req.Stdout, req.Stderr)
		if err != nil {
			return code, fmt.Errorf("executor: %w", err)
		}
		return code, nil
	}
	// Pipeline or redirect on host → bash -c.
	code, err := RunHostShell(ctx, pl.Raw, nil, req.Stdout, req.Stderr)
	if err != nil {
		return code, fmt.Errorf("executor: %w", err)
	}
	return code, nil
}

// runUniformContainer runs a pipeline where every segment routes to the container.
// A single simple segment (no redirect) runs as argv (mirroring runUniformHost).
// Multiple segments or a redirect run via bash -c on the pipeline raw.
func runUniformContainer(ctx context.Context, req Request, pl PipelineNode, rs []routedSeg) (int, error) {
	// Single simple segment → argv (not bash -c), mirroring host behavior.
	if len(rs) == 1 && !rs[0].seg.HasRedirect {
		if len(rs[0].seg.Args) == 0 {
			fmt.Fprintln(req.Stderr, "rejected: empty command")
			return 1, nil
		}
		env := resolveEnv(req.ContainerEnvPassthrough)
		code, err := req.ContainerRunner.RunContainer(ctx, rs[0].seg.Args, env, nil, req.Stdout, req.Stderr)
		if err != nil {
			fmt.Fprintf(req.Stderr, "container exec: %v\n", err)
			if code == 0 {
				code = 1
			}
			return code, nil
		}
		return code, nil
	}
	// Pipeline or redirect in container → bash -c the raw pipeline.
	return runContainerWhole(ctx, req, pl.Raw)
}

// runContainerWhole runs raw in the container via bash -c. Used for
// all-container pipelines with redirects, and fallback constructs ($(), backtick, &).
func runContainerWhole(ctx context.Context, req Request, raw string) (int, error) {
	if req.ContainerRunner == nil {
		fmt.Fprintln(req.Stderr, "no container configured: cannot route command to container")
		return 1, nil
	}
	argv := []string{"bash", "-c", raw}
	env := resolveEnv(req.ContainerEnvPassthrough)
	code, err := req.ContainerRunner.RunContainer(ctx, argv, env, nil, req.Stdout, req.Stderr)
	if err != nil {
		fmt.Fprintf(req.Stderr, "container exec: %v\n", err)
		if code == 0 {
			code = 1
		}
		return code, nil
	}
	return code, nil
}

// syncWriter wraps an io.Writer with a mutex so it is safe for concurrent use.
// Used to guard req.Stderr when multiple segment goroutines run in parallel.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// runMixedPipeline runs a pipeline whose segments span host and container,
// wiring stdout→stdin between adjacent segments with io.Pipe. All segments run
// concurrently; the exit code is that of the last segment.
//
// Pipe ownership and deadlock prevention: each segment goroutine closes BOTH
// ends of the pipes it owns when it finishes:
//   - writeEnds[i].Close()                      — sends EOF to segment i+1.
//   - readEnds[i].CloseWithError(ErrClosedPipe) — causes the upstream write
//     (via inter-segment io.PipeWriter) to return an error, which in turn
//     causes RunHost's io.Copy goroutine to close the OS stdoutPipe, delivering
//     SIGPIPE to the upstream OS subprocess so it exits and c.Wait() returns.
//
// This two-step unblocking (inter-segment pipe → OS pipe → subprocess exit)
// guarantees that an early-exiting downstream (e.g. `producer | head -1`)
// unblocks its upstream without leaving goroutines or processes running.
//
// req.Stderr is shared across all segment goroutines; it is wrapped in a
// syncWriter so concurrent writes from parallel segments do not race.
func runMixedPipeline(ctx context.Context, req Request, rs []routedSeg) (int, error) {
	n := len(rs)

	// Wrap stderr to allow safe concurrent writes from all segment goroutines.
	safeReq := req
	safeReq.Stderr = &syncWriter{w: req.Stderr}

	// stdin for each segment: first is nil, others read the previous pipe.
	stdins := make([]io.Reader, n)
	// stdout for each segment: last writes req.Stdout, others write a pipe.
	stdouts := make([]io.Writer, n)
	writeEnds := make([]*io.PipeWriter, n) // write end that segment i uses as stdout
	readEnds := make([]*io.PipeReader, n)  // read end that segment i uses as stdin (nil for i==0)

	for i := 0; i < n-1; i++ {
		pr, pw := io.Pipe()
		stdouts[i] = pw
		writeEnds[i] = pw
		stdins[i+1] = pr
		readEnds[i+1] = pr // segment i+1 owns this reader; closes it when done
	}
	stdouts[n-1] = req.Stdout

	exits := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range rs {
		go func(i int) {
			defer wg.Done()
			code, err := runSegment(ctx, safeReq, rs[i], stdins[i], stdouts[i])
			exits[i] = code
			if err != nil {
				fmt.Fprintf(safeReq.Stderr, "pipeline segment: %v\n", err)
				if exits[i] == 0 {
					exits[i] = 1
				}
			}
			// Close write end: signals EOF to the next segment.
			if writeEnds[i] != nil {
				writeEnds[i].Close()
			}
			// Close read end: unblocks the previous segment's inter-segment
			// io.Copy if it is still writing (e.g. this segment exited early
			// without draining stdin, as in `producer | head -1`).
			if readEnds[i] != nil {
				readEnds[i].CloseWithError(io.ErrClosedPipe)
			}
		}(i)
	}
	wg.Wait()
	return exits[n-1], nil
}

// runSegment runs a single routed segment with the given stdin/stdout. Its
// stderr always goes to req.Stderr. Redirect-bearing segments run via bash -c.
func runSegment(ctx context.Context, req Request, r routedSeg, stdin io.Reader, stdout io.Writer) (int, error) {
	if r.decision == "container" {
		argv := r.seg.Args
		if r.seg.HasRedirect {
			argv = []string{"bash", "-c", r.seg.Raw}
		}
		env := resolveEnv(req.ContainerEnvPassthrough)
		return req.ContainerRunner.RunContainer(ctx, argv, env, stdin, stdout, req.Stderr)
	}
	// host
	if r.seg.HasRedirect {
		return RunHostShell(ctx, r.seg.Raw, stdin, stdout, req.Stderr)
	}
	if len(r.seg.Args) == 0 {
		fmt.Fprintln(req.Stderr, "rejected: empty command")
		return 1, nil
	}
	return RunHost(ctx, r.seg.Args, stdin, stdout, req.Stderr)
}

func resolveEnv(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
