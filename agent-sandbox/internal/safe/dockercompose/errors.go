package dockercompose

import "errors"

// ErrResolve wraps any failure to resolve the Compose model (for example an
// invalid compose file or a docker error).
var ErrResolve = errors.New("safe docker-compose: resolve failed")
