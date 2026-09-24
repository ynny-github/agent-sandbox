package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Write places every fetched asset under dir. A destination that already
// exists is skipped, never rewritten: the skills are seeded once and the
// project owns them afterwards, and a profile a user has grown must not be
// replaced by an upgrade.
//
// Returned paths are Asset.Dest values, in Assets order.
func Write(dir string, fetched []Fetched) ([]string, []string, error) {
	var written, skipped []string
	for _, f := range fetched {
		dest := filepath.Join(dir, filepath.FromSlash(f.Asset.Dest))
		if _, err := os.Stat(dest); err == nil {
			skipped = append(skipped, f.Asset.Dest)
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, err)
		}
		if err := os.WriteFile(dest, f.Body, 0o644); err != nil {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, err)
		}
		written = append(written, f.Asset.Dest)
	}
	return written, skipped, nil
}
