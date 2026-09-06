package dockercompose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Mount is a normalized service mount from the resolved Compose model.
type Mount struct {
	Type   string `json:"type"`   // "bind", "volume", or "tmpfs"
	Source string `json:"source"` // absolute host path for bind; volume name for volume
	Target string `json:"target"`
}

// Service holds the security-relevant fields of one resolved service.
type Service struct {
	Privileged  bool              `json:"privileged"`
	NetworkMode string            `json:"network_mode"`
	Pid         string            `json:"pid"`
	Ipc         string            `json:"ipc"`
	UsernsMode  string            `json:"userns_mode"`
	CapAdd      []string          `json:"cap_add"`
	SecurityOpt []string          `json:"security_opt"`
	Devices     []json.RawMessage `json:"devices"`
	Volumes     []Mount           `json:"volumes"`
}

// Model is the resolved Compose project as emitted by `docker compose config`.
type Model struct {
	Services map[string]Service `json:"services"`
}

// DecodeModel parses `docker compose config --format json` output.
func DecodeModel(data []byte) (Model, error) {
	var m Model
	if err := json.Unmarshal(data, &m); err != nil {
		return Model{}, fmt.Errorf("decode compose config: %w", err)
	}
	return m, nil
}

// Resolver produces the canonical Compose model for the given global flags.
type Resolver interface {
	Resolve(ctx context.Context, globalFlags []string) (Model, error)
}

type execResolver struct{}

// NewResolver returns the default Resolver, which runs
// `docker compose <globalFlags> config --format json`.
func NewResolver() Resolver { return execResolver{} }

func (execResolver) Resolve(ctx context.Context, globalFlags []string) (Model, error) {
	args := append([]string{"compose"}, globalFlags...)
	args = append(args, "config", "--format", "json")

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Model{}, fmt.Errorf("docker compose config: %s", msg)
	}
	return DecodeModel(stdout.Bytes())
}
