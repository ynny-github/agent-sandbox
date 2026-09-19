package execd

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// A Job is one request. It owns the process groups that request created, and
// teardown is the same path whatever triggered it: the interpreter returning,
// the connection dropping, the timeout firing, or the client asking.
//
// One group per command, not one per request. A group's id is its leader's pid
// and the group ceases to exist when its last member exits, so a per-request
// group led by the request's first command would be empty by the time the
// second command tried to join it, and setpgid would fail with ESRCH. Keeping
// it alive would need an anchor process, and an anchor costs one policy launch
// per request — the exact resource launchPacer rations.
//
// Group membership is inherited across fork, which is what lets one killpg
// reach a command's grandchildren. It is not a hierarchy: a process that calls
// setpgid or setsid leaves, so a program that daemonises itself escapes. That
// is the accepted limit; the alternatives (walking /proc, cgroups, PID
// namespaces) are respectively racy and not available on macOS.
//
// A policy-controlled command's own children are on the far side of that
// limit. Measured on nono 0.74.0, 2026-09-20: `sh -c 'sleep 60'` under a
// 3000ms request timeout reports 124 and the sleep survives the session and
// keeps running — 3/3 runs, one stranded process each, still running after
// the session was gone (probe log set ss1-3). The 124 did not arrive at
// 3000ms: those runs returned at 5004-5005ms, because the request still pays
// DrainGrace on the way out (see ExitTimeout for that bound). The identical
// line as a floor command, `sleep 60` with no policy command in it, is killed
// at the timeout and leaves no survivor, 3/3, returning at 3002-3003ms with no
// drain to wait for (log set fs1-3). Piping the policy command from another
// one changes nothing: `git log --oneline | sh -c 'cat >/dev/null; sleep 60'`
// strands identically, 3/3 (log set lp1-3). So what escapes is whatever nono's
// shim puts between this process and the real command, not teardown failing in
// general. It has nothing to do with pipelines: the same strand happens with no
// pipe in the line at all.
type Job struct {
	mu     sync.Mutex
	groups []int
}

func NewJob() *Job { return &Job{} }

// jobKey is the context key a Job is carried under. Unexported, so only this
// package can put one on a context or read one back.
type jobKey struct{}

// withJob returns a context carrying j, for the exec handler to find.
func withJob(ctx context.Context, j *Job) context.Context {
	return context.WithValue(ctx, jobKey{}, j)
}

// jobFrom returns the Job on ctx, or nil when there is none (a caller that
// builds an interpreter by hand in a test).
func jobFrom(ctx context.Context) *Job {
	j, _ := ctx.Value(jobKey{}).(*Job)
	return j
}

// TerminateGrace is how long a process gets to act on SIGTERM before SIGKILL.
// It is long enough for a handler to run and short enough that a cancelled
// request still returns promptly. Terminate does not spend it when nothing is
// alive to spend it on.
const TerminateGrace = 200 * time.Millisecond

// DrainGrace bounds how long a finished command's output is waited for once
// its process tree is gone. Exceeding it means something outside the group
// still holds a write end; the request ends with a truncation note rather than
// hanging, which is what the old wiring did.
//
// This is the bound a pipeline with a policy-controlled command on both ends
// now runs into, and it is why this package no longer refuses that shape
// outright. Measured on nono 0.74.0 against this repository's
// command-profile.json, 2026-09-20, with `git log --oneline | git cat-file
// --batch-check` — the writer exits, the reader blocks on stdin — run 21
// times. Sixteen of those carried a 5000ms request timeout and five carried
// 20000ms; both are far above this grace, so the grace is the only bound in
// play either way. Probe log set: runB1-6, full, full2, bb1-10, plus three
// runs that reached the transcript only.
//
//	14 runs   the writer's drain never saw EOF: something on nono's side still
//	          held a duplicate of the write end, so waitDrains gave up and its
//	          forced close is what released the blocked reader. Requests ended
//	          at 2018-2023ms.
//	 7 runs   no stall at all; requests ended at 15-20ms.
//
// Only 9 of those 14 recorded per-stage timings — runs 1-6 of the set predate
// the instrumentation — and over those 9 the writer's own Wait returned in
// 14-16ms, waitDrains gave up at 2000-2001ms, and the reader's Wait then
// returned at 2018-2020ms. The stalled/clean split above needs no timing line,
// being read off elapsed time alone, which is why its denominator is 14 and
// this one is 9.
//
// All 21 returned. Three other aggregates have smaller denominators, because
// not every run recorded every figure: 18 of the 21 have a byte count, and all
// 18 are identical at 30585 bytes; 16 had a survivor check afterwards, and all
// 16 were clean. The remaining runs were not measured for those, which is not
// the same as having been measured clean.
//
// Three further run sets, each separate from those 21 and from each other.
// `git log --oneline | cat | git cat-file --batch-check`, a floor command
// between the two policy stages, 5 runs at a 5000ms timeout (log set cc1-5):
// 3 stalled, ending at 2023-2029ms, and 2 clean at 15-16ms, all complete.
// `git log --oneline | cat` — a policy writer into a floor reader — 8 runs
// (log set dd1-8): 14-19ms, 27361 bytes, and not one stall. That last set is
// the discriminator: the same writer stalls above and never stalls there, so
// what the stall takes is a policy-controlled *reader*, not merely piping a
// shim's output somewhere. The third set is the timeout leg (log set bt1-5),
// cited on ExitTimeout, and is deliberately not folded in here: those runs
// ended on a timeout mid-flight and cannot support a claim about complete
// output.
//
// Every figure above names the log set it came from, and that is deliberate.
// Three successive revisions of this comment carried numbers belonging to a
// neighbouring set — the 2028-2029ms pair above is exactly what leaked into
// the 21-run range twice. If you change a number here, re-derive it from the
// named set rather than from this comment's previous wording;
// docs/superpowers/probes/2026-09-19-policy-pipe-hang.md carries the per-run
// tables each log set above is named for.
//
// So the hazard is unchanged — the fd is still leaked, inside nono's own
// machinery — but it is a bounded incident now rather than a permanent one.
//
// What that split does and does not say about this constant's value. EOF
// either arrived within ~15ms or had not arrived by 2000ms; nothing landed in
// between. So shortening the grace would not make stalls rarer or commoner —
// it would only make each one cheaper. The hazard in shortening it is
// elsewhere: waitDrains ends a stall by force-closing the read ends, so a
// producer still writing at that moment loses its tail. That did not bite in
// any of the 18 runs that have a byte count — the producer had exited ~2s
// earlier and its bytes were already in the pipe — but a grace short enough to
// land while output is still in flight would truncate for real, and the note
// saying so is usually invisible (below). Raising it lengthens every stalled
// request by the same amount. Re-measure before moving it either way.
//
// That note is the last thing to know here: it does not reach the caller when
// it comes from a non-final pipeline stage, because mvdan.cc/sh drops a
// non-final stage's stderr entirely — measured, and reproduced with the
// library's own default exec handler, so it is not this package's wiring. A
// user therefore sees a ~2s stall with no explanation, complete output, and,
// unless the request timeout fired first, a correct exit status.
const DrainGrace = 2 * time.Second

// Start puts cmd in a process group of its own and starts it.
func (j *Job) Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return err
	}
	j.mu.Lock()
	j.groups = append(j.groups, cmd.Process.Pid)
	j.mu.Unlock()
	return nil
}

// Signal delivers sig to every group this job still has members in.
func (j *Job) Signal(sig syscall.Signal) {
	for _, pgid := range j.liveGroups() {
		syscall.Kill(-pgid, sig)
	}
}

// Terminate ends everything execd itself started: SIGTERM, a grace period,
// then SIGKILL for whatever ignored it. It returns at once when no group has a
// live member, which is the ordinary case — a command that finished cleanly
// leaves nothing behind, and every request would otherwise pay the grace
// period.
//
// "Everything execd started" is the whole guarantee, and it stops at nono's
// shim: a policy-controlled command's own children run inside nono's child
// sandbox, outside the groups this Job created, and killpg does not follow
// them there. Measured — see the Job doc comment above.
func (j *Job) Terminate() {
	if len(j.liveGroups()) == 0 {
		return
	}
	j.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(TerminateGrace)
	for time.Now().Before(deadline) {
		if len(j.liveGroups()) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	j.Signal(syscall.SIGKILL)
}

// liveGroups returns the groups that still have at least one member. Signal 0
// performs the permission and existence check without delivering anything.
//
// A group id is a reaped pid, so an id can in principle be recycled by an
// unrelated process that then creates a group of its own. The window for
// that is open from when the command is reaped until this Job's own
// Terminate next probes it — not "right after the command is reaped": Job
// teardown is per-request, not per-command, so in a line like `cmd1; sleep
// 10; cmd2` the pid recorded for cmd1 sits reaped, but still in j.groups,
// for as long as the rest of the request takes to run. What keeps this
// theoretical rather than reachable despite that longer window is pid
// allocation itself: it is sequential and the pid space is large, so a
// specific reaped pid being handed back out within one request's lifetime
// remains vanishingly unlikely. A PID namespace — the only real fix — is not
// available on both platforms.
func (j *Job) liveGroups() []int {
	j.mu.Lock()
	defer j.mu.Unlock()
	var live []int
	for _, pgid := range j.groups {
		if err := syscall.Kill(-pgid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			live = append(live, pgid)
		}
	}
	return live
}
