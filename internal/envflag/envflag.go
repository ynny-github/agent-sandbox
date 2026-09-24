// Package envflag loads environment variables from --env references and applies
// them to the current process. A reference is scheme-based: "file:<path>",
// "file://<path>", or a bare path all select a dotenv file. Applied values
// override the inherited host environment; with multiple references or
// duplicate keys, later ones win.
package envflag

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pair is a single KEY=VALUE parsed from an env file.
type pair struct {
	Key   string
	Value string
}

// Load resolves each ref, parses it as a dotenv file, and applies every pair
// with os.Setenv. Later refs and later duplicate keys override earlier ones,
// and all override the pre-existing host environment. It returns the
// de-duplicated key names it set, in first-seen order, so callers can expose
// them (e.g. to the nono profile). Load(nil) is a no-op returning (nil, nil).
func Load(refs []string) ([]string, error) {
	var keys []string
	seen := make(map[string]struct{})
	for _, ref := range refs {
		path, err := resolveRef(ref)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("envflag: read %s: %w", path, err)
		}
		pairs, err := parseDotenv(data)
		if err != nil {
			return nil, fmt.Errorf("envflag: parse %s: %w", path, err)
		}
		for _, p := range pairs {
			if err := os.Setenv(p.Key, p.Value); err != nil {
				return nil, fmt.Errorf("envflag: setenv %s: %w", p.Key, err)
			}
			if _, ok := seen[p.Key]; !ok {
				seen[p.Key] = struct{}{}
				keys = append(keys, p.Key)
			}
		}
	}
	return keys, nil
}

// resolveRef maps a --env reference to a filesystem path. It accepts
// "file://<path>", "file:<path>", and a bare path; a leading "~" expands to the
// home directory.
func resolveRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("envflag: empty --env reference")
	}
	if rest, ok := strings.CutPrefix(ref, "file://"); ok {
		return expandPath(rest), nil
	}
	if rest, ok := strings.CutPrefix(ref, "file:"); ok {
		return expandPath(rest), nil
	}
	return expandPath(ref), nil
}

// parseDotenv parses a minimal dotenv subset: one KEY=VALUE per line; blank
// lines and lines beginning with "#" are skipped; a leading "export " prefix is
// stripped; surrounding single or double quotes around the value are stripped.
// No variable interpolation. A non-blank, non-comment line without "=" (or with
// an empty key) is an error.
func parseDotenv(data []byte) ([]pair, error) {
	var pairs []pair
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid line (no '='): %q", line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("invalid line (empty key): %q", line)
		}
		pairs = append(pairs, pair{Key: key, Value: unquote(strings.TrimSpace(val))})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// unquote strips a single pair of matching surrounding quotes.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
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
