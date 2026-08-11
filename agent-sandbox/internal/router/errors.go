package router

import "errors"

// ErrSandboxNotRunning signals that a command was routed to the sandbox but
// the broker could not run it there. A CommandRunner returns it (possibly
// wrapped) so the router can print an actionable message instead of a raw
// broker error. Detect it with errors.Is.
var ErrSandboxNotRunning = errors.New("sandbox is not running")
