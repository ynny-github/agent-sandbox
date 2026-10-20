package execd

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// A Job is one request. It owns the process group the request's bash leads,
// and teardown is the same path whatever triggered it: bash exiting, the
// connection dropping, the timeout firing, or the client asking.
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
// the session was gone (probe log set ss1-3). The identical
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
	stdio  Stdio
}

// NewJob returns a Job for a request running on stdio.
func NewJob(stdio Stdio) *Job { return &Job{stdio: stdio} }

// Stdio returns the request's own three files.
func (j *Job) Stdio() Stdio { return j.stdio }

// jobKey is the context key a Job is carried under. Unexported, so only this
// package can put one on a context or read one back.
type jobKey struct{}

// withJob returns a context carrying j, for Run to start bash in.
func withJob(ctx context.Context, j *Job) context.Context {
	return context.WithValue(ctx, jobKey{}, j)
}

// jobFrom returns the Job on ctx, or nil when there is none (Run called
// directly, as tests do).
func jobFrom(ctx context.Context) *Job {
	j, _ := ctx.Value(jobKey{}).(*Job)
	return j
}

// TerminateGrace is how long a process gets to act on SIGTERM before SIGKILL.
// It is long enough for a handler to run and short enough that a cancelled
// request still returns promptly. Terminate does not spend it when nothing is
// alive to spend it on.
const TerminateGrace = 200 * time.Millisecond

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
