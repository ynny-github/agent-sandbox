// Package sandbox routes a command to drop/host/container and executes it,
// independent of any transport (MCP, CLI). Output is written to the caller's
// io.Writers.
package sandbox

import (
	"errors"
	"strings"
)

// ErrUnterminatedQuote is returned by ParseLine when a quote is never closed.
var ErrUnterminatedQuote = errors.New("unterminated quote")

const (
	quoteNone = iota
	quoteSingle
	quoteDouble
)

// isDoubleEscapable reports whether r is one of the characters a backslash
// escapes inside double quotes in POSIX shells.
func isDoubleEscapable(r rune) bool {
	return r == '"' || r == '\\' || r == '$' || r == '`'
}

// Line is a parsed command line: a sequence of pipelines joined by sequential
// operators. When Fallback is true the line contains a construct we do not
// split (command substitution or background &) and must run whole.
type Line struct {
	Raw       string
	Pipelines []PipelineNode
	Seps      []string // "&&" | "||" | ";"  (len == len(Pipelines)-1)
	Fallback  bool
}

// PipelineNode is a sequence of segments joined by "|".
type PipelineNode struct {
	Raw      string
	Segments []Segment
}

// Segment is a simple command, possibly carrying a redirect.
type Segment struct {
	Raw         string
	Args        []string
	HasRedirect bool
}

// ParseLine tokenizes raw into a structured Line. It returns
// ErrUnterminatedQuote for an unclosed quote.
func ParseLine(raw string) (Line, error) {
	line := Line{Raw: raw}

	// First pass: validate quotes and detect fallback constructs.
	if err := scanQuotes(raw, &line); err != nil {
		return Line{Raw: raw}, err
	}
	if line.Fallback {
		return line, nil
	}

	// Split into pipelines on top-level && || ; then each into segments on |.
	plRaws, seps := splitTop(raw, []string{"&&", "||", ";"})
	line.Seps = seps
	for _, plRaw := range plRaws {
		segRaws, _ := splitTop(plRaw, []string{"|"})
		pl := PipelineNode{Raw: plRaw}
		for _, segRaw := range segRaws {
			args, _ := tokenize(segRaw)
			pl.Segments = append(pl.Segments, Segment{
				Raw:         segRaw,
				Args:        args,
				HasRedirect: hasUnquotedRedirect(segRaw),
			})
		}
		line.Pipelines = append(line.Pipelines, pl)
	}
	return line, nil
}

// scanQuotes walks raw, returns ErrUnterminatedQuote on an unclosed quote, and
// sets line.Fallback if it finds an unquoted "$(", backtick, or a lone "&".
func scanQuotes(raw string, line *Line) error {
	runes := []rune(raw)
	quote := quoteNone
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch quote {
		case quoteSingle:
			if r == '\'' {
				quote = quoteNone
			}
			continue
		case quoteDouble:
			if r == '\\' && i+1 < len(runes) {
				i++
				continue
			}
			if r == '"' {
				quote = quoteNone
			}
			continue
		}
		switch r {
		case '\'':
			quote = quoteSingle
		case '"':
			quote = quoteDouble
		case '\\':
			i++ // skip escaped char
		case '`':
			line.Fallback = true
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				line.Fallback = true
			}
		case '&':
			// "&&" is a sequential operator; a lone "&" is background → fallback.
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			} else {
				line.Fallback = true
			}
		}
	}
	if quote != quoteNone {
		return ErrUnterminatedQuote
	}
	return nil
}

// splitTop splits raw at top-level (unquoted) occurrences of any separator in
// seps, longest-match-first. It returns the pieces (verbatim, including
// surrounding spaces) and the separators matched between them.
func splitTop(raw string, seps []string) (parts []string, matched []string) {
	runes := []rune(raw)
	quote := quoteNone
	var cur []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch quote {
		case quoteSingle:
			cur = append(cur, r)
			if r == '\'' {
				quote = quoteNone
			}
			continue
		case quoteDouble:
			cur = append(cur, r)
			if r == '\\' && i+1 < len(runes) {
				i++
				cur = append(cur, runes[i])
				continue
			}
			if r == '"' {
				quote = quoteNone
			}
			continue
		}
		switch r {
		case '\'':
			quote = quoteSingle
			cur = append(cur, r)
			continue
		case '"':
			quote = quoteDouble
			cur = append(cur, r)
			continue
		case '\\':
			cur = append(cur, r)
			if i+1 < len(runes) {
				i++
				cur = append(cur, runes[i])
			}
			continue
		}
		if sep, n := matchSep(runes, i, seps); sep != "" {
			parts = append(parts, string(cur))
			cur = nil
			matched = append(matched, sep)
			i += n - 1
			continue
		}
		cur = append(cur, r)
	}
	parts = append(parts, string(cur))
	return parts, matched
}

// matchSep returns the separator from seps starting at runes[i] and its rune
// length. For "|" it must not match the "||" operator.
func matchSep(runes []rune, i int, seps []string) (string, int) {
	for _, sep := range seps {
		sr := []rune(sep)
		if i+len(sr) > len(runes) {
			continue
		}
		ok := true
		for k, c := range sr {
			if runes[i+k] != c {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if sep == "|" && i+1 < len(runes) && runes[i+1] == '|' {
			continue // this is "||", not a pipe
		}
		return sep, len(sr)
	}
	return "", 0
}

// hasUnquotedRedirect reports whether seg contains an unquoted >, >>, <, or 2>.
func hasUnquotedRedirect(seg string) bool {
	runes := []rune(seg)
	quote := quoteNone
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch quote {
		case quoteSingle:
			if r == '\'' {
				quote = quoteNone
			}
			continue
		case quoteDouble:
			if r == '\\' && i+1 < len(runes) {
				i++
				continue
			}
			if r == '"' {
				quote = quoteNone
			}
			continue
		}
		switch r {
		case '\'':
			quote = quoteSingle
		case '"':
			quote = quoteDouble
		case '\\':
			i++
		case '>', '<':
			return true
		}
	}
	return false
}

// tokenize splits raw into argv tokens, resolving quotes/escapes. Operator
// characters are treated as ordinary text (callers use Raw for bash -c paths).
func tokenize(raw string) ([]string, error) {
	runes := []rune(raw)
	var args []string
	var cur strings.Builder
	inToken := false
	quote := quoteNone
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch quote {
		case quoteSingle:
			if r == '\'' {
				quote = quoteNone
			} else {
				cur.WriteRune(r)
			}
			inToken = true
			continue
		case quoteDouble:
			switch {
			case r == '\\' && i+1 < len(runes) && isDoubleEscapable(runes[i+1]):
				cur.WriteRune(runes[i+1])
				i++
			case r == '"':
				quote = quoteNone
			default:
				cur.WriteRune(r)
			}
			inToken = true
			continue
		}
		switch r {
		case '\'':
			quote = quoteSingle
			inToken = true
		case '"':
			quote = quoteDouble
			inToken = true
		case '\\':
			if i+1 < len(runes) {
				cur.WriteRune(runes[i+1])
				i++
			}
			inToken = true
		case ' ', '\t', '\n', '\r':
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args, nil
}
