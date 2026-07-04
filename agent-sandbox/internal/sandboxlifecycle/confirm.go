package sandboxlifecycle

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// ErrExternalAccessDeclined is returned when the user answers no to the
// unrestricted-network-access prompt.
var ErrExternalAccessDeclined = errors.New("external network access declined")

// ErrExternalAccessNeedsTTY is returned when allow_external is true but no
// interactive terminal is available to confirm it. There is no bypass.
var ErrExternalAccessNeedsTTY = errors.New("allow_external = true requires interactive confirmation, but no TTY is available")

// ConfirmExternalAccess gates unrestricted external network access behind a
// y/N prompt. When allowExternal is false it is a no-op. When true and
// interactive, it warns on out and reads the answer from in, returning nil only
// for "y"/"yes" and ErrExternalAccessDeclined otherwise. When true and not
// interactive, it returns ErrExternalAccessNeedsTTY.
func ConfirmExternalAccess(allowExternal, interactive bool, in io.Reader, out io.Writer) error {
	if !allowExternal {
		return nil
	}
	fmt.Fprintln(out, "WARNING: This sandbox has UNRESTRICTED external network access (allow_external = true).")
	fmt.Fprintln(out, "         The agent can reach any host on the internet.")
	if !interactive {
		return ErrExternalAccessNeedsTTY
	}
	fmt.Fprint(out, "Continue? [y/N]: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return ErrExternalAccessDeclined
	}
}

// DefaultExternalAccessConfirm returns a confirm callback for Ensure that reads
// from stdin and warns on stderr, treating a terminal-attached stdin as
// interactive.
func DefaultExternalAccessConfirm(cfg *config.Config) func() error {
	return func() error {
		interactive := term.IsTerminal(int(os.Stdin.Fd()))
		return ConfirmExternalAccess(cfg.Sandbox.Network.AllowExternal, interactive, os.Stdin, os.Stderr)
	}
}
