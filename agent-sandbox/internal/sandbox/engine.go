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
// fall back to the container whole-line path.
//
// NOTE: Task 7 will replace the interim mixed fallback with real io.Pipe wiring.
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
		// Interim: mixed host+container pipeline → run whole pipeline in container.
		// Task 7 replaces this with real io.Pipe wiring between host and container.
		if req.ContainerRunner == nil {
			fmt.Fprintln(req.Stderr, "no container configured: cannot route command to container")
			return 1, nil
		}
		return runContainerWhole(ctx, req, pl.Raw)
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

// runContainerWhole runs raw in the container via bash -c. Used for pipelines,
// redirects, fallback constructs ($(), backtick, &), and interim mixed pipelines.
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
