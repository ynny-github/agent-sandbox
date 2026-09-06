package dockercompose

import (
	"context"
	"fmt"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/safe"
)

// Prepare runs the full safety pipeline for a docker compose invocation.
//
// It parses args, applies CLI-level rules (which short-circuit before any
// docker process runs), resolves the canonical model, and applies model-level
// rules. The returned slice lists every violation found; an empty slice means
// the invocation is safe to execute. A non-nil error indicates an operational
// failure (the model could not be resolved) and the caller must run nothing.
func Prepare(ctx context.Context, args []string, cwd string, r Resolver) ([]safe.Violation, error) {
	parsed := ParseArgs(args)

	// --help never executes anything, so let it fall straight through to the
	// real binary instead of resolving a model no help output needs. Without
	// this, a bare "compose --help" would still shell out to
	// "realdocker compose --help config --format json" — measured: real
	// docker treats the "--help" ahead of "config" as help for "config"
	// specifically and prints that (exit 0), not JSON, so DecodeModel would
	// fail and the agent would see an opaque decode error instead of help.
	if parsed.HelpRequested {
		return nil, nil
	}

	if v := CheckCLI(parsed); len(v) > 0 {
		return v, nil
	}

	model, err := r.Resolve(ctx, parsed.GlobalFlags)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResolve, err)
	}

	return CheckModel(model, cwd), nil
}
