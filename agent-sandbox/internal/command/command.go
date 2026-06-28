// Package command parses a raw command line into argv tokens, detecting
// unquoted shell operators without interpreting them.
package command

import (
	"errors"
	"strings"
)

// ErrUnterminatedQuote is returned by Parse when a quote is never closed.
var ErrUnterminatedQuote = errors.New("unterminated quote")

// Command is a parsed command line.
type Command struct {
	Raw         string   // original input, verbatim
	Args        []string // argv tokens, quotes/escapes resolved
	HasOperator bool     // true if an unquoted shell operator is present
}

const (
	quoteNone = iota
	quoteSingle
	quoteDouble
)

// Parse tokenizes raw into a Command. It returns ErrUnterminatedQuote for a
// command line with an unclosed quote; otherwise the error is nil.
func Parse(raw string) (Command, error) {
	cmd := Command{Raw: raw}
	var args []string
	var cur strings.Builder
	inToken := false
	quote := quoteNone

	runes := []rune(raw)
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
		case ';', '&', '|', '<', '>', '`':
			cmd.HasOperator = true
			cur.WriteRune(r)
			inToken = true
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				cmd.HasOperator = true
			}
			cur.WriteRune(r)
			inToken = true
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}

	if quote != quoteNone {
		return Command{Raw: raw}, ErrUnterminatedQuote
	}
	if inToken {
		args = append(args, cur.String())
	}
	cmd.Args = args
	return cmd, nil
}

// isDoubleEscapable reports whether r is one of the characters a backslash
// escapes inside double quotes in POSIX shells.
func isDoubleEscapable(r rune) bool {
	return r == '"' || r == '\\' || r == '$' || r == '`'
}
