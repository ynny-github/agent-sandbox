// main.go
//
// The entrypoint lives at the module root so the install path stays short:
// `go install github.com/ynny-github/agent-sandbox@latest`. Everything else
// stays under agent-sandbox/, where the internal/ tree remains importable only
// from within that subtree — this file reaches the CLI through the non-internal
// cmd package and touches nothing else.
package main

import (
	"os"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
