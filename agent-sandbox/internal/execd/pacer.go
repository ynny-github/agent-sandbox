package execd

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PolicyLaunchBurst and PolicyLaunchInterval pace how fast policy-controlled
// commands may be launched: PolicyLaunchBurst of them may start with no wait
// at all, and the budget refills at one launch per PolicyLaunchInterval. See
// launchPacer for what is being rationed and how the numbers were arrived at.
const (
	PolicyLaunchBurst    = 4
	PolicyLaunchInterval = 500 * time.Millisecond
)

// launchPacer rations how fast policy-controlled commands are launched.
//
// The defect it exists for, measured on nono 0.77.0 / Ubuntu 24.04 against
// this repository's command-profile.json: policy-controlled commands launched
// back-to-back inside ONE nono session exhaust something the session reclaims
// only over time, and every launch past that point dies before the command
// itself runs, with "Sandbox initialization failed: tool-sandbox shim failed
// to connect to <TMPDIR>/nono-tool-sandbox-<id>/supervisor.sock: Operation
// not permitted". Measured with `git --version` run 12 times through xargs
// inside one session:
//
//	~50ms apart   ooooooxxxxxx / ooooooxxxxxx / oooooxoxxxxx   (3 runs)
//	250ms apart   oooooxoooooo
//	500ms apart   oooooooooooo / oooooooooooo / oooooooooooo   (3 runs)
//
// The same 12 commands run as 12 SEPARATE `nono run` sessions are 12/12, so
// what is exhausted belongs to the session, not to the host — and execd
// is one long-lived session by construction (it must be the session
// entrypoint for the profile to govern everything it executes, and nono
// refuses to nest), so spacing the launches is the lever this side of the
// boundary actually has.
//
// The numbers: PolicyLaunchInterval is the smallest spacing measured clean,
// and PolicyLaunchBurst is set below the 5-6 launches measured free from a
// cold start, so a burst cannot spend the whole budget. They are not read
// from nono, which reports nothing about this budget; re-measure before
// changing them, and re-measure on a nono upgrade.
//
// Only execd's own launches are paced, and only those need to be.
// Measured against this profile with a synthetic `bash -> git` edge (the real
// profile's own nesting edges are `bash`/`sh` -> `go` and `git` -> `ssh`,
// neither cheap to drive here): a policy command launching 12 more policy
// commands from inside its own child sandbox is 12/12, and session-level
// launches issued immediately afterwards are 3/3 — 3 runs of both. What is
// exhausted is spent by launches made from the session, which is exactly the
// set execHandler makes. A different nesting shape was not measured, so treat
// this as "the shape this repository can produce", not as a law.
//
// Not the trigger, despite an earlier note in this repository saying so: the
// session's top-level "network" block. With every network key removed from
// the profile — top-level and child alike — the same burst still fails
// identically (3/3 runs). That earlier finding was measured on nono 0.74.0
// and does not hold on 0.77.0.
type launchPacer struct {
	mu sync.Mutex
	// ready is when the next launch may start. It runs ahead of the clock
	// while launches are being spent and is clamped back to at most a full
	// burst behind it, which is what "the bucket refills up to
	// PolicyLaunchBurst" means expressed as a single instant rather than a
	// token count and a refill timer.
	ready time.Time
}

// wait blocks until this launch's turn, or until ctx is done. It reports
// whether the launch may proceed; false means ctx ended the wait and the
// caller must not start the command.
//
// The turn is claimed under the lock and waited for outside it, so concurrent
// callers queue in the order they arrived rather than all racing for the same
// instant.
func (p *launchPacer) wait(ctx context.Context) bool {
	p.mu.Lock()
	now := time.Now()
	if floor := now.Add(-(PolicyLaunchBurst - 1) * PolicyLaunchInterval); p.ready.Before(floor) {
		p.ready = floor
	}
	at := p.ready
	p.ready = p.ready.Add(PolicyLaunchInterval)
	p.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// isPolicyControlledPath reports whether path is a nono-generated shim for a
// policy-controlled command, rather than a floor command's real binary,
// judging only by the resolved path's own shape. This is a heuristic over
// nono's own directory naming ("<TMPDIR>/nono-tool-sandbox-<id>/shims/<name>",
// measured directly), not a documented API: nono gives this process no other
// signal that distinguishes the two tiers from a resolved path alone. A
// future nono that renames this directory would make this heuristic stop
// matching, and the failure mode is silent: every command would be treated as
// a floor command, so nothing would be paced and the launch-rate defect above
// would come back.
//
// command_policies is back in the profile this repository ships: git, ssh,
// bash and sh each carry a command_policies entry, so nono generates a real
// shim for each of the four and PATH resolves them there, not to their real
// binaries. This function is therefore live against real shims, not inert.
func isPolicyControlledPath(path string) bool {
	shimsDir := filepath.Dir(path)
	return filepath.Base(shimsDir) == "shims" &&
		strings.Contains(filepath.Base(filepath.Dir(shimsDir)), "nono-tool-sandbox-")
}
