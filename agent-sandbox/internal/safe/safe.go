// Package safe holds shared helpers for the "safe" command wrappers.
package safe

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Violation describes a single reason an invocation was refused.
// Source is "cli" for command-line rules or "compose" for resolved-model rules.
// Service is the Compose service name, or "" when the rule is not service-scoped.
type Violation struct {
	Source  string
	Service string
	Setting string
}

func (v Violation) String() string {
	if v.Service == "" {
		return fmt.Sprintf("%s: %s", v.Source, v.Setting)
	}
	return fmt.Sprintf("%s:%s: %s", v.Source, v.Service, v.Setting)
}

// PathWithin reports whether target is root itself or a descendant of root.
// Both paths are cleaned lexically before comparison; callers that need
// symlinks resolved should pass paths through RealPath first.
func PathWithin(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

// RealPath returns p as an absolute path with symlinks resolved. When the leaf
// does not exist yet (so it cannot be resolved directly), it resolves symlinks
// on the longest existing ancestor and re-appends the remaining suffix. This
// keeps two paths under the same real directory comparable even when the target
// has not been created yet (e.g. a bind-mount source directory).
func RealPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)

	rest := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without resolving anything; fall back.
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
