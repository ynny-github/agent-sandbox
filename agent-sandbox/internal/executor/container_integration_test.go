//go:build integration

package executor_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
)

const testProjectName = "mcptest"
const testServiceName = "app"

const testComposeYAML = `services:
  app:
    image: ubuntu:latest
    command: ["tail", "-f", "/dev/null"]
`

const testWorkspaceComposeYAML = `services:
  workspace:
    image: ubuntu:latest
    command: ["tail", "-f", "/dev/null"]
`

func newIntegrationDockerCli(t *testing.T) command.Cli {
	t.Helper()
	cli, err := command.NewDockerCli()
	if err != nil {
		t.Skipf("docker cli unavailable: %v", err)
	}
	if err := cli.Initialize(cliflags.NewClientOptions()); err != nil {
		t.Skipf("docker cli initialize: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Client().Ping(pingCtx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	return cli
}

// testProject builds a minimal *types.Project for RunContainer tests.
// It only needs the project name — the container is started separately via CLI.
func testProject(name string) *composetypes.Project {
	return &composetypes.Project{Name: name}
}

func startComposeService(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte(testComposeYAML), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	up := exec.Command("docker", "compose", "-f", composeFile, "-p", testProjectName, "up", "-d", "--wait")
	if out, err := up.CombinedOutput(); err != nil {
		t.Skipf("docker compose up failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composeFile, "-p", testProjectName, "down", "--remove-orphans").Run()
	})
}

func TestRunContainer_Echo_WritesStdoutAndExitsZero(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	ex := executor.NewComposeExecutor(cli, testProject(testProjectName))

	var stdout, stderr bytes.Buffer
	code, err := ex.RunContainer(context.Background(), testServiceName, []string{"echo", "hello"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestRunContainer_BothOutputs_WrittenToCorrectWriters(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	ex := executor.NewComposeExecutor(cli, testProject(testProjectName))

	var stdout, stderr bytes.Buffer
	code, err := ex.RunContainer(context.Background(), testServiceName,
		[]string{"bash", "-c", "printf out && printf err 1>&2"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err")
	}
}

func TestRunContainer_ShellOperators_ExecutedInsideContainer(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	ex := executor.NewComposeExecutor(cli, testProject(testProjectName))

	var stdout, stderr bytes.Buffer
	code, err := ex.RunContainer(context.Background(), testServiceName,
		[]string{"bash", "-c", "ls / | head -1"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("stdout should contain directory listing output")
	}
}

func TestComposeExecutor_IsRunning(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte(testWorkspaceComposeYAML), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	projectName := "isrunning-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ex := executor.NewComposeExecutor(cli, testProject(projectName), "", "")
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composeFile, "-p", projectName, "down", "--remove-orphans").Run()
	})

	running, err := ex.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning before compose up returned error: %v", err)
	}
	if running {
		t.Fatal("IsRunning before compose up = true, want false")
	}

	up := exec.Command("docker", "compose", "-f", composeFile, "-p", projectName, "up", "-d", "--wait")
	if out, err := up.CombinedOutput(); err != nil {
		t.Skipf("docker compose up failed: %v\n%s", err, out)
	}

	running, err = ex.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning after compose up returned error: %v", err)
	}
	if !running {
		t.Fatal("IsRunning after compose up = false, want true")
	}
}

func TestRunContainer_NonZeroExit_ReturnsExitCode(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	ex := executor.NewComposeExecutor(cli, testProject(testProjectName))

	var stdout, stderr bytes.Buffer
	code, err := ex.RunContainer(context.Background(), testServiceName,
		[]string{"bash", "-c", "printf out; printf err 1>&2; exit 2"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err")
	}
}

func TestCleanStale_RemovesOrphanedSandboxNetwork(t *testing.T) {
	cli := newIntegrationDockerCli(t)

	// Create a cr-sandbox-* network with no containers — simulates a zombie network left
	// behind by a crashed process. PID 0 is never valid, so "cr-sandbox-0" is safe.
	resp, err := cli.Client().NetworkCreate(context.Background(), "cr-sandbox-0_prunetest", dockernetwork.CreateOptions{})
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	netID := resp.ID
	t.Cleanup(func() {
		cli.Client().NetworkRemove(context.Background(), netID)
	})

	// nil project is intentional: CleanStale only uses e.dockerCLI, not e.project.
	ex := executor.NewComposeExecutor(cli, nil, "", "")
	result, err := ex.CleanStale(context.Background())
	if err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	if result.Networks < 1 {
		t.Errorf("Networks = %d, want >= 1", result.Networks)
	}

	_, inspectErr := cli.Client().NetworkInspect(context.Background(), netID, dockernetwork.InspectOptions{})
	if !errdefs.IsNotFound(inspectErr) {
		t.Errorf("network should have been removed, got inspect err: %v", inspectErr)
	}
}

func TestCleanStale_ForceRemovesManagedRunningContainersAndSandboxNetworks(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	dir := t.TempDir()
	unique := "cleanstale-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	projectName := unique
	networkName := "cr-sandbox-" + unique + "-prunetest"
	composeFile := filepath.Join(dir, "docker-compose.yml")
	composeYAML := fmt.Sprintf(`services:
  workspace:
    image: ubuntu:latest
    command: ["tail", "-f", "/dev/null"]
    labels:
      cr.managed: "true"
    networks:
      - sandbox
networks:
  sandbox:
    name: %s
`, networkName)
	if err := os.WriteFile(composeFile, []byte(composeYAML), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composeFile, "-p", projectName, "down", "--remove-orphans").Run()
		if network, err := cli.Client().NetworkInspect(context.Background(), networkName, dockernetwork.InspectOptions{}); err == nil {
			cli.Client().NetworkRemove(context.Background(), network.ID)
		}
	})

	up := exec.Command("docker", "compose", "-f", composeFile, "-p", projectName, "up", "-d", "--wait")
	if out, err := up.CombinedOutput(); err != nil {
		t.Skipf("docker compose up failed: %v\n%s", err, out)
	}

	containersBefore, err := cli.Client().ContainerList(context.Background(), dockercontainer.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "cr.managed=true"),
			filters.Arg("label", "com.docker.compose.project="+projectName),
			filters.Arg("status", "running"),
		),
	})
	if err != nil {
		t.Fatalf("list managed running containers before CleanStale: %v", err)
	}
	if len(containersBefore) == 0 {
		t.Fatal("compose project did not create a running cr.managed=true container")
	}
	if _, err := cli.Client().NetworkInspect(context.Background(), networkName, dockernetwork.InspectOptions{}); err != nil {
		t.Fatalf("network %q should exist before CleanStale: %v", networkName, err)
	}

	ex := executor.NewComposeExecutor(cli, testProject(projectName), "", "")
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cleanCancel()
	result, err := ex.CleanStale(cleanCtx)
	if err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	if result.Containers < 1 {
		t.Fatalf("CleanStale removed %d containers, want at least 1", result.Containers)
	}
	if result.Networks < 1 {
		t.Fatalf("CleanStale removed %d networks, want at least 1", result.Networks)
	}

	containersAfter, err := cli.Client().ContainerList(context.Background(), dockercontainer.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "cr.managed=true"),
			filters.Arg("label", "com.docker.compose.project="+projectName),
		),
	})
	if err != nil {
		t.Fatalf("list managed containers after CleanStale: %v", err)
	}
	if len(containersAfter) != 0 {
		t.Fatalf("managed container still listed after CleanStale: %d", len(containersAfter))
	}
	_, inspectErr := cli.Client().NetworkInspect(context.Background(), networkName, dockernetwork.InspectOptions{})
	if !errdefs.IsNotFound(inspectErr) {
		t.Fatalf("network %q should have been removed, got inspect err: %v", networkName, inspectErr)
	}
}

func TestRunContainer_Timeout_Returns124AndProcessTerminated(t *testing.T) {
	cli := newIntegrationDockerCli(t)
	startComposeService(t)
	ex := executor.NewComposeExecutor(cli, testProject(testProjectName))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := ex.RunContainer(ctx, testServiceName, []string{"bash", "-c", "echo partial; sleep 60"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
	if stdout.String() != "partial\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "partial\n")
	}

	time.Sleep(200 * time.Millisecond)
	var psOut bytes.Buffer
	checkCode, checkErr := ex.RunContainer(context.Background(), testServiceName,
		[]string{"bash", "-c", "ps aux | grep 'sleep 60' | grep -v grep"}, nil, &psOut, &bytes.Buffer{})
	if checkErr != nil {
		t.Fatalf("process check error: %v", checkErr)
	}
	if checkCode == 0 {
		t.Fatalf("timed-out process still running: %q", psOut.String())
	}
}
