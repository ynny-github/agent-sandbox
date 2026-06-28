package sandbox_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandbox"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		args []string
		op   bool
	}{
		{"plain", "git status", []string{"git", "status"}, false},
		{"doubleQuotedSpaces", `git commit -m "fix bug"`, []string{"git", "commit", "-m", "fix bug"}, false},
		{"singleQuoted", `echo 'a b'`, []string{"echo", "a b"}, false},
		{"backslashEscape", `echo a\ b`, []string{"echo", "a b"}, false},
		{"semicolon", "pwd; whoami", []string{"pwd;", "whoami"}, true},
		{"andand", "a && b", []string{"a", "&&", "b"}, true},
		{"pipe", "ls | wc", []string{"ls", "|", "wc"}, true},
		{"redirect", "cat > f", []string{"cat", ">", "f"}, true},
		{"subst", "echo $(id)", []string{"echo", "$(id)"}, true},
		{"backtick", "echo `id`", []string{"echo", "`id`"}, true},
		{"background", "a & b", []string{"a", "&", "b"}, true},
		{"quotedSemicolon", `echo "a;b"`, []string{"echo", "a;b"}, false},
		{"singleQuotedPipe", `echo 'a|b'`, []string{"echo", "a|b"}, false},
		{"dollarVarNotFlagged", "echo $HOME", []string{"echo", "$HOME"}, false},
		{"empty", "", nil, false},
		{"whitespaceOnly", "   ", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := sandbox.Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.args) {
				t.Errorf("Args = %#v, want %#v", cmd.Args, tt.args)
			}
			if cmd.HasOperator != tt.op {
				t.Errorf("HasOperator = %v, want %v", cmd.HasOperator, tt.op)
			}
			if cmd.Raw != tt.in {
				t.Errorf("Raw = %q, want %q", cmd.Raw, tt.in)
			}
		})
	}
}

func TestParse_UnterminatedQuote(t *testing.T) {
	if _, err := sandbox.Parse(`echo "hi`); !errors.Is(err, sandbox.ErrUnterminatedQuote) {
		t.Fatalf("err = %v, want ErrUnterminatedQuote", err)
	}
	if _, err := sandbox.Parse(`echo 'hi`); !errors.Is(err, sandbox.ErrUnterminatedQuote) {
		t.Fatalf("err = %v, want ErrUnterminatedQuote", err)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		pipelines [][]string // per pipeline: each segment's Raw (trimmed)
		seps      []string
		redirect  [][]bool   // per pipeline: each segment's HasRedirect
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
		{"mixedSeqPipe", "a | b && c", [][]string{{"a ", " b "}, {" c"}}, []string{"&&"}, [][]bool{{false, false}, {false}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := sandbox.ParseLine(tt.in)
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
