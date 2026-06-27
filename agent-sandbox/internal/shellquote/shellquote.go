// Package shellquote quotes strings as single shell tokens.
package shellquote

import "strings"

// Quote returns s wrapped as a single POSIX shell token. A shell parsing the
// result yields exactly the original string as one argument.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
