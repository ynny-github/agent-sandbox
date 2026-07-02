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

	if v := CheckCLI(parsed); len(v) > 0 {
		return v, nil
	}

	model, err := r.Resolve(ctx, parsed.GlobalFlags)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResolve, err)
	}

	return CheckModel(model, cwd), nil
}
