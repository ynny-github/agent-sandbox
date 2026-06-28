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
