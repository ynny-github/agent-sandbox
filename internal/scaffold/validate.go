package scaffold

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runCommand is a seam so tests do not need nono installed. It mirrors the
// same pattern in internal/cli/doctor.go.
var runCommand = defaultRunCommand

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Validate hands every fetched profile to `nono profile validate` before any
// file is written. What it covers is narrow: the templates are validated on
// main before merge and init does not transform them, so the case left is the
// operator's nono differing from the developer's. It is kept because it costs
// one subprocess call against a dependency agent-sandbox already requires.
//
// It is not a security check. validate inspects syntax, field names, enum
// values and group references; it does not look at what a profile grants.
//
// Warnings are returned rather than treated as failures. The command template
// raises [allow_all_network] on ssh by design.
func Validate(ctx context.Context, fetched []Fetched) ([]string, error) {
	dir, err := os.MkdirTemp("", "agent-sandbox-init-")
	if err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)

	var warnings []string
	for _, f := range fetched {
		if !f.Asset.Profile {
			continue
		}
		// nono reads a path, not bytes, so the candidate is written to a
		// temporary directory that never becomes the project.
		path := filepath.Join(dir, filepath.Base(f.Asset.Dest))
		if err := os.WriteFile(path, f.Body, 0o600); err != nil {
			return nil, fmt.Errorf("stage %s for validation: %w", f.Asset.Dest, err)
		}
		out, err := runCommand(ctx, "nono", "profile", "validate", path)
		if err != nil {
			return nil, fmt.Errorf("%s: nono profile validate: %w: %s", f.Asset.Dest, err, strings.TrimSpace(string(out)))
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "[warn]") {
				warnings = append(warnings, f.Asset.Dest+": "+strings.TrimSpace(line))
			}
		}
	}
	return warnings, nil
}
