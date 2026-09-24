// cmd/agent-sandbox/main.go
//
// Every binary this module ships lives at cmd/<command-name>/: `go install`
// names the binary after the main package's directory, and there is no -o for
// `go install <pkg>@version`, so the directory name IS the command name.
// Shared code lives in the module-root internal/ tree, which every main
// package here can import. The safe command wrappers are reserved for
// cmd/safe/safe-<tool>/, so that `go install <mod>/cmd/safe/...@latest`
// installs those and nothing else.
package main

import (
	"os"

	"github.com/ynny-github/agent-sandbox/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
