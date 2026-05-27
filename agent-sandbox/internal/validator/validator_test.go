package validator_test

import (
	"errors"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/validator"
)

func TestValidate_Pipe_ReturnsError(t *testing.T) {
	err := validator.Validate("git log | head -20")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_RedirectOut_ReturnsError(t *testing.T) {
	err := validator.Validate("ls > /tmp/out.txt")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_RedirectIn_ReturnsError(t *testing.T) {
	err := validator.Validate("cat < /etc/passwd")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_FdToFd_2to1_ReturnsError(t *testing.T) {
	err := validator.Validate("git status 2>&1")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_FdToFd_1to2_ReturnsError(t *testing.T) {
	err := validator.Validate("git status 1>&2")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_Semicolon_ReturnsError(t *testing.T) {
	err := validator.Validate("rm -rf / ; echo done")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_CleanCommand_ReturnsNil(t *testing.T) {
	if err := validator.Validate("git status"); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

func TestValidate_CommandSubstitution_ReturnsError(t *testing.T) {
	err := validator.Validate("echo $(whoami)")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_Backtick_ReturnsError(t *testing.T) {
	err := validator.Validate("echo `whoami`")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_Ampersand_NotFdToFd_ReturnsError(t *testing.T) {
	err := validator.Validate("cmd1 & cmd2")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_FdToFd_WithPipe_ReturnsError(t *testing.T) {
	err := validator.Validate("git log 2>&1 | head -20")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_FdToFdPrefixOfLargerRedirect_ReturnsError(t *testing.T) {
	err := validator.Validate("git status 2>&10")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}

func TestValidate_FdToFdSuffixOfLargerRedirect_ReturnsError(t *testing.T) {
	err := validator.Validate("git status 12>&1")
	if !errors.Is(err, validator.ErrShellOperator) {
		t.Errorf("got %v, want ErrShellOperator", err)
	}
}
