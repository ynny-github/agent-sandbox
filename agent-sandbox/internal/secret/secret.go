// Package secret resolves and loads secrets referenced from config. A reference
// is either a bare filesystem path or a scheme URI (e.g. "file://…"); the scheme
// selects the Source. Only the file source is implemented now; new schemes
// (e.g. "op://") add a case without changing callers.
package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source reads a single resolved secret value.
type Source interface {
	Load() (string, error)
}

// FileSource loads the secret from a file.
type FileSource struct{ Path string }

// Load reads the file, trims surrounding whitespace, and errors if the file is
// missing or the trimmed content is empty.
func (s FileSource) Load() (string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("secret: read %s: %w", s.Path, err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("secret: file %s is empty", s.Path)
	}
	return v, nil
}

// Resolve inspects ref and returns the matching Source. A ref containing "://"
// is parsed as <scheme>://<rest>; otherwise the whole ref is a file path. Path
// forms expand a leading "~" to the home directory.
func Resolve(ref string) (Source, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("secret: empty reference")
	}
	scheme, rest, hasScheme := strings.Cut(ref, "://")
	if !hasScheme {
		return FileSource{Path: expandPath(ref)}, nil
	}
	switch scheme {
	case "file":
		return FileSource{Path: expandPath(rest)}, nil
	default:
		return nil, fmt.Errorf("secret: unsupported scheme %q (only file is supported)", scheme)
	}
}

// expandPath expands a leading "~" to the user's home directory; other paths are
// returned unchanged (relative paths resolve against the process cwd).
func expandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
