package router_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestParseLine_UnterminatedQuote(t *testing.T) {
	if _, err := router.ParseLine(`echo "hi`); !errors.Is(err, router.ErrUnterminatedQuote) {
		t.Fatalf("err = %v, want ErrUnterminatedQuote", err)
	}
	if _, err := router.ParseLine(`echo 'hi`); !errors.Is(err, router.ErrUnterminatedQuote) {
		t.Fatalf("err = %v, want ErrUnterminatedQuote", err)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		pipelines [][]string // per pipeline: each segment's Raw (trimmed)
		seps      []string
		redirect  [][]bool // per pipeline: each segment's HasRedirect
		fallback  bool
	}{
		{"plain", "git status", [][]string{{"git status"}}, nil, [][]bool{{false}}, false},
		{"pipe", "ls | wc", [][]string{{"ls ", " wc"}}, nil, [][]bool{{false, false}}, false},
		{"andSeq", "a && b", [][]string{{"a "}, {" b"}}, []string{"&&"}, [][]bool{{false}, {false}}, false},
		{"orSeq", "a || b", [][]string{{"a "}, {" b"}}, []string{"||"}, [][]bool{{false}, {false}}, false},
		{"semi", "a ; b", [][]string{{"a "}, {" b"}}, []string{";"}, [][]bool{{false}, {false}}, false},
		{"redirect", "cat foo > out", [][]string{{"cat foo > out"}}, nil, [][]bool{{true}}, false},
		{"pipeRedirect", "a | b > f", [][]string{{"a ", " b > f"}}, nil, [][]bool{{false, true}}, false},
		{"quotedPipe", `echo "a|b" | c`, [][]string{{`echo "a|b" `, " c"}}, nil, [][]bool{{false, false}}, false},
		{"subst", "echo $(id)", [][]string{{"echo $(id)"}}, nil, [][]bool{{false}}, true},
		{"backtick", "echo `id`", [][]string{{"echo `id`"}}, nil, [][]bool{{false}}, true},
		{"background", "a & b", [][]string{{"a & b"}}, nil, [][]bool{{false}}, true},
		{"redirectStderrDup", "echo hi 2>&1", [][]string{{"echo hi 2>&1"}}, nil, [][]bool{{true}}, false},
		{"redirectFdToStderr", "echo hi >&2", [][]string{{"echo hi >&2"}}, nil, [][]bool{{true}}, false},
		{"redirectAmpToFile", "echo hi &>out", [][]string{{"echo hi &>out"}}, nil, [][]bool{{true}}, false},
		{"mixedSeqPipe", "a | b && c", [][]string{{"a ", " b "}, {" c"}}, []string{"&&"}, [][]bool{{false, false}, {false}}, false},
		// An unquoted newline separates commands exactly like ";". Before this
		// was handled it fell through to tokenize as ordinary whitespace, so the
		// second command silently became arguments of the first.
		{"newlineSeq", "a\nb", [][]string{{"a"}, {"b"}}, []string{";"}, [][]bool{{false}, {false}}, false},
		{"newlineSeqWithArgs", "echo a\necho b", [][]string{{"echo a"}, {"echo b"}}, []string{";"}, [][]bool{{false}, {false}}, false},
		// A newline with nothing after it is not a separator: the empty tail is
		// dropped rather than becoming an empty pipeline.
		{"trailingNewline", "a\n", [][]string{{"a"}}, nil, [][]bool{{false}}, false},
		{"leadingNewline", "\na", [][]string{{"a"}}, nil, [][]bool{{false}}, false},
		{"blankLineBetween", "a\n\nb", [][]string{{"a"}, {"b"}}, []string{";"}, [][]bool{{false}, {false}}, false},
		// A newline inside quotes is data, not a separator.
		{"quotedNewline", "echo 'a\nb'", [][]string{{"echo 'a\nb'"}}, nil, [][]bool{{false}}, false},
		// A heredoc body is data too and its newlines must not be split on, so
		// the whole line falls back to running in a single shell.
		{"heredoc", "cat <<'EOF'\nhi\nEOF", nil, nil, nil, true},
		{"heredocDash", "cat <<-EOF\nhi\nEOF", nil, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := router.ParseLine(tt.in)
			if err != nil {
				t.Fatalf("ParseLine(%q) error: %v", tt.in, err)
			}
			if line.Fallback != tt.fallback {
				t.Fatalf("Fallback = %v, want %v", line.Fallback, tt.fallback)
			}
			if tt.fallback {
				return // structure not asserted on fallback
			}
			if len(line.Pipelines) != len(tt.pipelines) {
				t.Fatalf("pipelines = %d, want %d", len(line.Pipelines), len(tt.pipelines))
			}
			for i, pl := range line.Pipelines {
				for j, seg := range pl.Segments {
					if seg.Raw != tt.pipelines[i][j] {
						t.Errorf("pl%d seg%d Raw = %q, want %q", i, j, seg.Raw, tt.pipelines[i][j])
					}
					if seg.HasRedirect != tt.redirect[i][j] {
						t.Errorf("pl%d seg%d HasRedirect = %v, want %v", i, j, seg.HasRedirect, tt.redirect[i][j])
					}
				}
			}
			if !reflect.DeepEqual(line.Seps, tt.seps) {
				t.Errorf("Seps = %#v, want %#v", line.Seps, tt.seps)
			}
		})
	}
}
