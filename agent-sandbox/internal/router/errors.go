package router

import "errors"

// ErrSandboxNotRunning signals that a command was routed to the sandbox
// container but the container is not running. A ContainerRunner returns it
// (possibly wrapped) so the router can print an actionable message instead of
// a raw Docker/Compose error. Detect it with errors.Is.
var ErrSandboxNotRunning = errors.New("sandbox is not running")
