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
// The existence check and the write are one atomic operation
// (O_WRONLY|O_CREATE|O_EXCL) rather than a stat followed by a separate
// write. os.Stat follows symlinks, so a dangling symlink at dest would read
// as fs.ErrNotExist and a subsequent os.WriteFile would then write through
// it -- potentially outside the project directory entirely. O_EXCL refuses
// to open through an existing entry, symlink or not, and closes that
// stat/write race in the process.
//
// Returned paths are Asset.Dest values, in Assets order.
func Write(dir string, fetched []Fetched) ([]string, []string, error) {
	var written, skipped []string
	for _, f := range fetched {
		dest := filepath.Join(dir, filepath.FromSlash(f.Asset.Dest))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, err)
		}
		file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				skipped = append(skipped, f.Asset.Dest)
				continue
			}
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, err)
		}
		_, werr := file.Write(f.Body)
		cerr := file.Close()
		if werr != nil {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, werr)
		}
		if cerr != nil {
			return written, skipped, fmt.Errorf("%s: %w", f.Asset.Dest, cerr)
		}
		written = append(written, f.Asset.Dest)
	}
	return written, skipped, nil
}
