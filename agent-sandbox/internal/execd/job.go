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
// its process tree is gone. Exceeding it means a process outside the group
// holds a write end; the request ends with a truncation note rather than
// hanging, which is what the old wiring did.
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

// Terminate ends everything this job started: SIGTERM, a grace period, then
// SIGKILL for whatever ignored it. It returns at once when no group has a live
// member, which is the ordinary case — a command that finished cleanly leaves
// nothing behind, and every request would otherwise pay the grace period.
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
