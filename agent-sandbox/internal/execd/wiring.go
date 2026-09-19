package execd

import (
	"io"
	"os"
	"os/exec"

	"mvdan.cc/sh/v3/interp"
)

// outputDrain copies one command's output pipe into the interpreter's writer.
type outputDrain struct {
	rc   io.ReadCloser
	done chan struct{}
}

// startDrain launches the copy goroutine and returns immediately.
func startDrain(w io.Writer, rc io.ReadCloser) outputDrain {
	d := outputDrain{rc: rc, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		// Closing the read end when the copy fails is what delivers EPIPE to a
		// writer whose reader has already exited (`seq … | head -n 1`).
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
		}
	}()
	return d
}

// wait blocks until the copy has drained to EOF. It must run after cmd.Start
// and before cmd.Wait (see execHandler), and it does not close w: the
// interpreter's writer may still be alive after this one command — a later
// command in the same pipeline stage writes to it too (`{ echo one; echo
// two; } | cat`), or the caller holds it open across the whole request — and
// only the interpreter, which knows when the whole script is done with it,
// may end its lifetime. What ends this copy is the child closing its own copy
// of the pipe's write end when it exits.
func (d outputDrain) wait() { <-d.done }

// abort is for a command that never gets to run at all: it closes the read
// end so a copy goroutine blocked on Read (or one that would never see data)
// unblocks with EOF or an error, then waits for it to exit. Without this, a
// later pipe's setup failing after an earlier one succeeded — or cmd.Start
// itself failing — would leak a goroutine parked on Read forever, since
// nothing would ever close its end of the pipe otherwise.
func (d outputDrain) abort() {
	d.rc.Close()
	d.wait()
}

// interposeOutputs wires stdout and stderr through os/exec pipes and returns
// the drains the caller must wait on after Start and before Wait.
func interposeOutputs(cmd *exec.Cmd, hc interp.HandlerContext) ([]outputDrain, error) {
	passthrough := func(w io.Writer) bool {
		// A real file needs no interposition: closing it is not ours to do, and
		// the shim holding a duplicate of it is harmless.
		return w == nil || w == os.Stdout || w == os.Stderr
	}

	if interfaceEqual(hc.Stdout, hc.Stderr) && !passthrough(hc.Stdout) {
		// `2>&1`, or a caller that hands one writer for both streams. Wire
		// exactly one pipe and one copy goroutine: os/exec makes the same
		// guarantee when a caller assigns Stdout and Stderr the same writer
		// directly — "if Stdout and Stderr are the same writer, at most one
		// goroutine at a time will call Write" — by reusing a single fd, and
		// this preserves that guarantee by construction (one goroutine only)
		// rather than by synchronizing two independent copies into one writer.
		rc, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = cmd.Stdout
		return []outputDrain{startDrain(hc.Stdout, rc)}, nil
	}

	var drains []outputDrain
	if passthrough(hc.Stdout) {
		cmd.Stdout = hc.Stdout
	} else {
		rc, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		drains = append(drains, startDrain(hc.Stdout, rc))
	}
	if passthrough(hc.Stderr) {
		cmd.Stderr = hc.Stderr
	} else {
		rc, err := cmd.StderrPipe()
		if err != nil {
			// The stdout drain above, if any, is already running; abort it
			// rather than leaving its goroutine parked on Read forever, since
			// this command will now never start.
			for _, d := range drains {
				d.abort()
			}
			return nil, err
		}
		drains = append(drains, startDrain(hc.Stderr, rc))
	}
	return drains, nil
}

// interfaceEqual mirrors the unexported helper os/exec itself uses to detect
// when Stdout and Stderr are the same writer. Comparing two interface values
// with == panics only when they share a dynamic type that is not comparable;
// no writer execd deals with does, but recovering keeps that fact from
// ever being load-bearing.
func interfaceEqual(a, b any) (eq bool) {
	defer func() {
		if recover() != nil {
			eq = false
		}
	}()
	return a == b
}
