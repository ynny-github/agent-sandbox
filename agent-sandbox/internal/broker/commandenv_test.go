package broker_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// childEnvFor drives the whole production path that decides a brokered
// command's environment — config → sandboxhost.ResolveCommand → EnvAllowVars →
// NewNonoExecutor → broker.Server → broker.Client → child process — and returns
// what the child actually received.
//
// Testing ProcessEnv in isolation is not enough: the regression this guards
// against (sandbox.command.env_passthrough silently becoming a no-op) left
// every unit test green, because each layer looked correct on its own and only
// the seam between them was broken.
func childEnvFor(t *testing.T, cfg *config.Config) string {
	t.Helper()

	// The client sends its own cwd, which the executor validates.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	resolved, err := sandboxhost.ResolveCommand(cfg, cwd)
	if err != nil {
		t.Fatalf("ResolveCommand() error = %v", err)
	}
	profilePath, cleanupProfile, err := resolved.WriteProfile()
	if err != nil {
		t.Fatalf("WriteProfile() error = %v", err)
	}
	t.Cleanup(cleanupProfile)

	// A stub standing in for nono that dumps the environment it was handed.
	// Absolute paths only, so it works even with an empty environment.
	stub := writeStub(t, "#!/bin/sh\n/usr/bin/env\n")
	sock := startTestServer(t,
		broker.NewNonoExecutor(stub, profilePath, cwd, resolved.EnvAllowVars()))

	var out, errb testBuffer
	code, err := broker.NewClient(sock).RunSandboxed(
		context.Background(), []string{"printenv"}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunSandboxed() error = %v (stderr: %s)", err, errb.String())
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errb.String())
	}
	return out.String()
}

// A variable listed in sandbox.command.env_passthrough and present in the
// launcher's environment must reach the command; one that is not listed must
// not. The launcher is the only workable source: the agent's own nono profile
// does not allow-list env_passthrough, so these variables are stripped long
// before the sandboxed router could report them back to the broker.
func TestEnvPassthroughVariableReachesTheCommand(t *testing.T) {
	t.Setenv("ASB_TEST_FORWARDED", "yes")
	t.Setenv("ASB_TEST_WITHHELD", "no")

	cfg := &config.Config{}
	cfg.Sandbox.Command.EnvPassthrough = []string{"ASB_TEST_FORWARDED"}

	env := childEnvFor(t, cfg)
	if !strings.Contains(env, "ASB_TEST_FORWARDED=yes") {
		t.Errorf("env_passthrough variable never reached the command; child env:\n%s", env)
	}
	if strings.Contains(env, "ASB_TEST_WITHHELD") {
		t.Errorf("a variable outside env_passthrough reached the command; child env:\n%s", env)
	}
}

// The mise capability grants "MISE*" / "__MISE*" on the command profile. Those
// are prefix patterns, and the broker must satisfy them from the launcher's
// environment or the grant is dead on arrival.
func TestMiseCapabilityEnvReachesTheCommand(t *testing.T) {
	t.Setenv("MISE_SHELL", "fish")
	t.Setenv("__MISE_SESSION", "abc")
	t.Setenv("MISCELLANEOUS", "no")

	cfg := &config.Config{}
	cfg.Sandbox.Host.Capabilities = []string{"mise"}

	env := childEnvFor(t, cfg)
	for _, want := range []string{"MISE_SHELL=fish", "__MISE_SESSION=abc"} {
		if !strings.Contains(env, want) {
			t.Errorf("mise capability grant unsatisfied: %q missing from child env:\n%s", want, env)
		}
	}
	if strings.Contains(env, "MISCELLANEOUS=") {
		t.Errorf("MISE* over-matched a similarly named variable; child env:\n%s", env)
	}
}

// baselineEnv lives in internal/sandboxhost and reaches the child only through
// the profile's allow_vars. This is also the guard against the two lists
// drifting apart: the broker no longer keeps a copy of its own.
func TestCommandProfileBaselineEnvReachesTheCommand(t *testing.T) {
	t.Setenv("TERM", "xterm-asb-test")

	env := childEnvFor(t, &config.Config{})
	for _, want := range []string{"PATH=", "HOME=", "TERM=xterm-asb-test"} {
		if !strings.Contains(env, want) {
			t.Errorf("baseline variable %q missing from child env:\n%s", want, env)
		}
	}
}

// The broker socket variable is deliberately absent from the command profile
// (agentOnlyEnv), so a brokered command must not be able to recurse into the
// broker.
func TestBrokerSocketVarNeverReachesTheCommand(t *testing.T) {
	t.Setenv(broker.SocketEnvVar, "/tmp/should-not-propagate.sock")

	env := childEnvFor(t, &config.Config{})
	if strings.Contains(env, broker.SocketEnvVar) {
		t.Errorf("%s reached a brokered command; child env:\n%s", broker.SocketEnvVar, env)
	}
}
