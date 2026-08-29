package router

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
// operators. Pipelines are separated by && || ; or a newline; a newline is
// reported as ";". When Fallback is true the line contains a construct we do
// not split (command substitution, background &, or a heredoc) and must run
// whole.
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

	// Split into pipelines on top-level && || ; newline, then each into segments
	// on |. A newline is a command terminator in shell grammar, so it must split
	// here; left to tokenize it would read as ordinary whitespace and silently
	// turn the next command into arguments of the previous one.
	plRaws, seps := splitTop(raw, []string{"&&", "||", ";", "\n"})
	plRaws, seps = dropBlankParts(plRaws, seps)
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
// sets line.Fallback if it finds an unquoted "$(", backtick, "<<", or a lone
// "&".
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
		case '<':
			// "<<" opens a heredoc whose body is data: its newlines are not
			// command terminators and its contents must not be routed. Splitting
			// would tear the body apart, so the line runs whole.
			if i+1 < len(runes) && runes[i+1] == '<' {
				line.Fallback = true
				i++
			}
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				line.Fallback = true
			}
		case '&':
			switch {
			case i+1 < len(runes) && runes[i+1] == '&':
				i++ // "&&" sequential operator
			case i > 0 && (runes[i-1] == '>' || runes[i-1] == '<'):
				// ">&" / "<&" fd-duplication redirect (e.g. 2>&1, >&2) — not background.
			case i+1 < len(runes) && runes[i+1] == '>':
				// "&>" / "&>>" redirect (e.g. &>file) — not background.
			default:
				// A lone "&" is background → fallback. Note: a backslash-escaped
				// ">" immediately before a real background "&" (e.g. `\>&`) is
				// mis-read as a redirect here; that combination is vanishingly
				// rare and acceptable for this heuristic parser.
				line.Fallback = true
			}
		}
	}
	if quote != quoteNone {
		return ErrUnterminatedQuote
	}
	return nil
}

// dropBlankParts removes whitespace-only pipelines produced by the split and
// rewrites the separator list to stay aligned with what is left. Blank parts
// are routine rather than exceptional: a trailing newline, a blank line between
// commands, and a newline after "&&" all produce one.
//
// The separator kept across a run of blanks is the *first* of the run, so
// "a &&\nb" stays a conditional rather than degrading into "a ; b". A newline
// separator is normalized to ";" so callers only ever see the three operators
// they already handle.
//
// When every part is blank the raw line is kept as the single part, leaving the
// empty-command rejection to the executor instead of silently succeeding.
func dropBlankParts(parts, seps []string) ([]string, []string) {
	var keptParts, keptSeps []string
	var pending string
	havePending := false
	for i, part := range parts {
		if i > 0 && !havePending {
			pending, havePending = seps[i-1], true
		}
		if strings.TrimSpace(part) == "" {
			continue
		}
		if len(keptParts) > 0 {
			if pending == "\n" {
				pending = ";"
			}
			keptSeps = append(keptSeps, pending)
		}
		keptParts = append(keptParts, part)
		havePending = false
	}
	if len(keptParts) == 0 {
		return parts[:1], nil
	}
	return keptParts, keptSeps
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
