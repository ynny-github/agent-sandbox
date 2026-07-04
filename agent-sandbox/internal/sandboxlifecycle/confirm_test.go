package sandboxlifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxlifecycle"
)

func TestConfirmExternalAccess_DisabledIsNoop(t *testing.T) {
	var out strings.Builder
	if err := sandboxlifecycle.ConfirmExternalAccess(false, false, strings.NewReader(""), &out); err != nil {
		t.Errorf("err = %v, want nil when allow_external is false", err)
	}
	if out.Len() != 0 {
		t.Errorf("no output expected when disabled, got %q", out.String())
	}
}

func TestConfirmExternalAccess_YesContinues(t *testing.T) {
	for _, in := range []string{"y\n", "yes\n", "Y\n", "YES\n"} {
		var out strings.Builder
		if err := sandboxlifecycle.ConfirmExternalAccess(true, true, strings.NewReader(in), &out); err != nil {
			t.Errorf("input %q: err = %v, want nil", in, err)
		}
		if !strings.Contains(out.String(), "UNRESTRICTED external network access") {
			t.Errorf("input %q: warning not shown:\n%s", in, out.String())
		}
	}
}

func TestConfirmExternalAccess_NoOrEmptyDeclines(t *testing.T) {
	for _, in := range []string{"n\n", "no\n", "\n", "xyz\n"} {
		var out strings.Builder
		err := sandboxlifecycle.ConfirmExternalAccess(true, true, strings.NewReader(in), &out)
		if !errors.Is(err, sandboxlifecycle.ErrExternalAccessDeclined) {
			t.Errorf("input %q: err = %v, want ErrExternalAccessDeclined", in, err)
		}
	}
}

func TestConfirmExternalAccess_NonInteractiveAborts(t *testing.T) {
	var out strings.Builder
	err := sandboxlifecycle.ConfirmExternalAccess(true, false, strings.NewReader("y\n"), &out)
	if !errors.Is(err, sandboxlifecycle.ErrExternalAccessNeedsTTY) {
		t.Errorf("err = %v, want ErrExternalAccessNeedsTTY", err)
	}
}
