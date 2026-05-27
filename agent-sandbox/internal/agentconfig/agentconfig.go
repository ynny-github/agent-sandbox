package agentconfig

import (
	"fmt"
	"io"
	"strings"
)

const snippet = `## Command Router

When native shell commands are unavailable, use the ` + "`run_command`" + ` MCP tool instead.
Allowed commands run on the host; all others run in an isolated container.
Output is written to files; read the returned paths for stdout/stderr.
`

// All formats currently emit the same snippet; format is validated for future differentiation.
var supportedFormats = []string{"claude", "agents", "gemini"}

func Print(format string, w io.Writer) error {
	for _, f := range supportedFormats {
		if f == format {
			_, err := fmt.Fprint(w, snippet)
			return err
		}
	}
	return fmt.Errorf("unknown format %q: supported formats are %s", format, strings.Join(supportedFormats, ", "))
}
