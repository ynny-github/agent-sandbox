// agent-sandbox/main.go
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
