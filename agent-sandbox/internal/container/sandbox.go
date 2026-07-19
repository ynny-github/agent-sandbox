package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/docker/cli/cli/command"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// NormalizeProjectName converts a directory name to a Docker Compose project name.
func NormalizeProjectName(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func ProjectSandboxName(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	base := NormalizeProjectName(filepath.Base(abs))
	if base == "" {
		base = "workspace"
	}
	sum := sha256.Sum256([]byte(abs))
	return "cr-sandbox-" + base + "-" + hex.EncodeToString(sum[:])[:10]
}

// DetectProjectNetwork checks whether a Docker network named "<cwd-project>_<suffix>" exists.
// Returns the full network name if found, "" otherwise.
// An empty suffix means "join no project network": it returns "" without
// inspecting, so the sandbox attaches to a compose network only when one is
// explicitly configured via sandbox.container.external_network.
func DetectProjectNetwork(ctx context.Context, dockerCLI command.Cli, suffix string) string {
	if suffix == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	projectName := NormalizeProjectName(filepath.Base(cwd))
	if projectName == "" {
		return ""
	}
	networkName := projectName + "_" + suffix
	_, inspectErr := dockerCLI.Client().NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{})
	if inspectErr != nil {
		if !errdefs.IsNotFound(inspectErr) {
			slog.Warn("sandbox: network inspect error", "network", networkName, "error", inspectErr)
		}
		return ""
	}
	return networkName
}
