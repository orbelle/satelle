package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// TestMain re-execs this test binary as the satelle CLI when
// SATELLE_CLI_REEXEC=1, so an end-to-end test can hand a child process a real
// `satelle` without building one. Every other run falls straight through to
// the package's tests.
func TestMain(m *testing.M) {
	if os.Getenv("SATELLE_CLI_REEXEC") == "1" {
		// A test that fakes a release installs this binary as "satelle <version>".
		if v := os.Getenv("SATELLE_TEST_VERSION"); v != "" {
			buildinfo.Version = v
		}
		// A test that runs `satelle pr` as a child points its forge at a fake.
		if u := os.Getenv(testForgeAPIURLEnv); u != "" {
			forgeAPIURL = u
		}
		initGateRun()
		root := NewRootCmd()
		root.SetArgs(os.Args[1:])
		c, err := root.ExecuteC()
		if c != nil {
			closeAppForCmd(c)
		}
		err, show := finishExecute(err)
		if err != nil && show {
			fmt.Fprintln(os.Stderr, err)
		}
		finishGateRun(err)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// The suite runs with no terminal, which is an agent-facing caller: every
	// gated command would hand off to a detached copy of the test binary. The
	// hand-off has its own tests and turns itself on there (gatecaller_test.go);
	// everything else keeps the synchronous path it was written against.
	_ = os.Setenv(gateModeEnv, string(gateModeInteractive))
	// The same holds for the session-start release refresh: os.Executable is
	// this test binary, so a real spawn would run a detached copy of the suite.
	// Its decision is tested through releaseAdvisoryAt's injected spawner.
	releaseRefresh = func() {}
	// And for the substrate refresh, for the same reason (substrateAdvisory).
	substrateRefresh = func(string) {}
	// And for the command-policy refresh (policyAdvisory).
	policyRefresh = func(string) {}
	// And for init's push of what it changed (publishInitChanges). Its binding is
	// tested through initPushCmd; the push itself in-process (init_push_test.go).
	initPush = func(io.Writer, string) error { return nil }
	// Isolate gate/seat tests from the AMBIENT process environment: when this
	// test binary itself runs under a real satelle dispatch (a named-agent
	// step, or — as when this test was written — a rework-relay coder session
	// working on THIS story), SATELLE_DISPATCH_*/SATELLE_RELAY_* are already
	// set on the host process. hookDenyReason and friends read them via
	// os.Getenv with no test-injection seam, so an unrelated gate test that
	// never calls t.Setenv would otherwise silently pick up a REAL dispatch
	// marker and take the relay-deny branch instead of the plain one it
	// asserts on (sty_752c4ef2 rework-relay review round). Clear them once
	// here so every test starts from a clean marker state; a test that wants
	// one sets it itself via t.Setenv.
	for _, k := range []string{
		config.DispatchAgentEnv, config.DispatchStepEnv, config.DispatchItemEnv,
		config.RelayBindingEnv, config.RelayItemEnv, config.SpawnEnv,
		// The session id a harness exports to its shell: the suite runs inside
		// one, and ResolveSession now reads it (sty_36d6f155), so an unrelated
		// test that expects no ambient session would otherwise resolve ours.
		"CLAUDE_CODE_SESSION_ID",
		// A parent dispatch's scratch dir would put every t.TempDir() under the
		// scratch root, where init skips workspace registration (sty_e3a1c1d5).
		config.ScratchEnv,
	} {
		_ = os.Unsetenv(k)
	}
	os.Exit(runIsolated(m))
}

// runIsolated runs the package's tests with XDG_CONFIG_HOME pointing at a
// process-lifetime temp dir. Any command a test drives through runRoot reads the
// per-user credentials file (the assignee lookup in openAppForCmd), so a test
// that never isolates it would otherwise resolve the operator's real
// ~/.config/satelle/credentials.toml — or write it, if it saves a credential
// (sty_18403814). A test that needs its own credential store still sets
// XDG_CONFIG_HOME with t.Setenv, which overrides this backstop.
func runIsolated(m *testing.M) int {
	dir, err := os.MkdirTemp("", "satelle-cli-xdg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "satelle cli tests: temp XDG_CONFIG_HOME:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	_ = os.Setenv("XDG_CONFIG_HOME", dir)
	// A node join a test runs writes its secrets hand-off under the user cache
	// dir (remotedef.HandoffPath); keep it off the operator's.
	cache := filepath.Join(dir, "cache")
	_ = os.Setenv("XDG_CACHE_HOME", cache)
	recordGateDelivered = restoringRecorder(recordGateDelivered)
	// The push tests exercise push MECHANICS over throwaway fixtures (a skill
	// named x whose body is "x") that are not valid substrate, so the validation a
	// push runs (sty_89b9df20) is stood down for them. The tests of that
	// validation call useRealPushValidator.
	realPushValidator = pushValidator
	pushValidator = func(io.Writer, string) int { return 0 }
	// A scope resolve asks the account service; the suite answers from
	// suiteAccount instead of a hosted server.
	installSuiteAccount()
	return m.Run()
}

// testForgeAPIURLEnv names the fake forge a re-exec'd child's `satelle pr`
// talks to (forgeAPIURL).
const testForgeAPIURLEnv = "SATELLE_TEST_FORGE_API_URL"

// realPushValidator is the validation a push really runs, kept aside while the
// rest of the suite runs with it stood down.
var realPushValidator func(io.Writer, string) int

// useRealPushValidator restores the real push validation for one test.
func useRealPushValidator(t *testing.T) {
	t.Helper()
	prev := pushValidator
	pushValidator = realPushValidator
	t.Cleanup(func() { pushValidator = prev })
}
