package execd

import (
	"io"
	"os"
	"os/exec"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// wiring owns every fd a command's output travels through. The Job, not
// os/exec, holds them: when cmd.Stdout is an *os.File that os/exec did not
// create, os/exec starts no copier goroutine and Wait closes nothing, so the
// "Start, then drain, then Wait, never any other order" rule this package used
// to state and obey stops being a rule and becomes a situation that cannot
// arise.
//
// The same ownership covers stdin, for the same reason from the other
// direction: cmd.Stdin being an *os.File is what keeps Wait from blocking on
// an os/exec copier parked in Read with the upstream end of the pipeline still
// open.
type wiring struct {
	// inherited is the request's own stdio. Identity against it is what tells a
	// descriptor the client passed from a pipe the interpreter made.
	inherited [3]*os.File
	// jobEnds are this side's copies of the fds the child was given: the write
	// end of each output pipe, and the read end of an interposed stdin pipe.
	// They exist only so exec.Cmd can duplicate them into the child, and must
	// be closed the moment it has.
	jobEnds []*os.File
	drains  []*drain
}

type drain struct {
	r    *os.File
	done chan struct{}
}

// startDrain launches the copy goroutine and returns immediately. The
// goroutine ends one of three ways: the pipe reaches EOF (every copy of its
// write end closed, this side's included), the copy fails, or waitDrains
// force-closes the read end below when the grace runs out. The third is not an
// edge case — against a policy-controlled reader it is how the copy ends in
// most runs, because nono keeps a duplicate of the write end and the EOF never
// comes. See DrainGrace.
//
// It does not close w: the interpreter's writer may still be alive after this
// one command — a later command in the same pipeline stage writes to it too
// (`{ echo one; echo two; } | cat`), or the caller holds it open across the
// whole request — and only the interpreter, which knows when the whole script
// is done with it, may end its lifetime.
func startDrain(w io.Writer, r *os.File) *drain {
	d := &drain{r: r, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		// Closing the read end is what delivers EPIPE to a child still writing
		// into a pipe whose output has nowhere left to go (`seq … | head -n 1`,
		// where the interpreter has closed the next stage's reader). At EOF it
		// is this side simply dropping an fd it owns and no longer needs.
		defer d.r.Close()
		io.Copy(w, r)
	}()
	return d
}

// passthrough reports whether x can be handed to the child as-is.
//
// Two kinds qualify. The request's own three files qualify by identity,
// whatever they are: the client passed them, nothing inside execd waits on
// their EOF, and a passed pipe was measured released on time by nono's shim
// (see the spec's spike). Files the interpreter opened for a redirect qualify
// by kind — the shell already decided where those bytes go.
//
// What must never qualify is one of the interpreter's own pipes. nono's shim
// duplicates every descriptor it is given and keeps the copy, so a child handed
// the interpreter's pipe writer leaves the next stage waiting for an EOF that
// only arrives when the shim exits. Identity is checked before kind precisely
// because both an inherited descriptor and an interpreter pipe can be a FIFO.
func (w *wiring) passthrough(x any) (*os.File, bool) {
	f, ok := x.(*os.File)
	if !ok {
		return nil, false
	}
	for _, in := range w.inherited {
		if in != nil && f == in {
			return f, true
		}
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	if mode := fi.Mode(); !mode.IsRegular() && mode&os.ModeCharDevice == 0 {
		return nil, false
	}
	return f, true
}

func (w *wiring) wireOutputs(cmd *exec.Cmd, hc interp.HandlerContext) error {
	attach := func(dst io.Writer) (*os.File, error) {
		if f, ok := w.passthrough(dst); ok {
			return f, nil
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		w.jobEnds = append(w.jobEnds, pw)
		w.drains = append(w.drains, startDrain(dst, pr))
		return pw, nil
	}

	if hc.Stdout == nil && hc.Stderr == nil {
		return nil
	}
	// interp.StdIO already substitutes io.Discard for a nil writer, so this is
	// only for an interpreter built by hand with one of the two left nil: a
	// pipe must always have somewhere to copy to.
	if hc.Stdout == nil {
		hc.Stdout = io.Discard
	}
	if hc.Stderr == nil {
		hc.Stderr = io.Discard
	}
	if interfaceEqual(hc.Stdout, hc.Stderr) {
		// 2>&1, or one writer for both: one pipe and one drain, so at most one
		// goroutine ever writes to that writer. os/exec makes the same
		// guarantee for an aliased writer — "if Stdout and Stderr are the same
		// writer, at most one goroutine at a time will call Write" — and this
		// preserves it by construction rather than by synchronizing two copies.
		f, err := attach(hc.Stdout)
		if err != nil {
			w.abort()
			return err
		}
		cmd.Stdout, cmd.Stderr = f, f
		return nil
	}
	so, err := attach(hc.Stdout)
	if err != nil {
		w.abort()
		return err
	}
	se, err := attach(hc.Stderr)
	if err != nil {
		w.abort()
		return err
	}
	cmd.Stdout, cmd.Stderr = so, se
	return nil
}

// wireStdin gives the child its input under the same ownership rule.
//
// A real file (`cmd < f.txt`) goes straight to cmd.Stdin: the shell opened it,
// the shim duplicating it is harmless, and os/exec starts no copier for an
// *os.File. Anything else — a pipeline stage's read end, or the pipe
// interp.StdIO made out of the caller's reader — gets a pipe this side owns,
// because the shim keeps a duplicate of every fd it is handed and a duplicate
// of the interpreter's own read end would leave the upstream writer with no
// reader to get EPIPE from, blocking it on a pipe nobody drains.
//
// The copy goroutine is deliberately not joined: the request must not wait for
// an upstream that has not finished writing. It ends when hc.Stdin reaches EOF
// or the write fails — the child exiting closes the last read end of the pipe,
// and the interpreter closes hc.Stdin when the pipeline stage it belongs to is
// done — and it closes the write end on the way out, which is the child's EOF.
func (w *wiring) wireStdin(cmd *exec.Cmd, hc interp.HandlerContext) error {
	if hc.Stdin == nil {
		return nil
	}
	if f, ok := w.passthrough(hc.Stdin); ok {
		cmd.Stdin = f
		return nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdin = pr
	w.jobEnds = append(w.jobEnds, pr)
	src := hc.Stdin
	go func() {
		defer pw.Close()
		io.Copy(pw, src)
	}()
	return nil
}

// closeJobEnds drops this side's copies of the fds the child was given, which
// must happen right after Start: while the job holds one of the write ends, the
// drain's EOF can never arrive.
func (w *wiring) closeJobEnds() {
	for _, f := range w.jobEnds {
		f.Close()
	}
	w.jobEnds = nil
}

// waitDrains waits for every copy to reach EOF, bounded. It reports whether it
// gave up.
//
// Giving up is not evidence that a process escaped the group. The holder
// measured in practice was never in it: nono's own machinery retains a
// duplicate of the write end on the far side of the shim, where this package
// has no reach and no visibility. A process that left the group could hold one
// too. Either way the request must end, so this bound is what ends it — and
// against a policy-controlled reader it is the ordinary path, not the
// exceptional one. See DrainGrace.
func (w *wiring) waitDrains(timeout time.Duration) (truncated bool) {
	deadline := time.After(timeout)
	for _, d := range w.drains {
		select {
		case <-d.done:
		case <-deadline:
			// Closing every read end ends the copies that are still running,
			// so no goroutine is left parked on a pipe this request has
			// stopped waiting for.
			for _, rest := range w.drains {
				rest.r.Close()
			}
			return true
		}
	}
	return false
}

// abort tears the wiring down for a command that will not run. Closing the read
// ends is what unblocks a drain that would otherwise wait forever on a pipe
// whose writer is never going to exist.
func (w *wiring) abort() {
	for _, f := range w.jobEnds {
		f.Close()
	}
	w.jobEnds = nil
	for _, d := range w.drains {
		d.r.Close()
		<-d.done
	}
	w.drains = nil
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
