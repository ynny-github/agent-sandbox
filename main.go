// main.go
//
// The entrypoint. The cobra command tree it runs lives elsewhere in this
// module; the shared packages live in the module-root internal/ tree, which
// every main package in this module can import.
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
