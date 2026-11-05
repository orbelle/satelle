package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/verb"
)

// runRoot executes a fresh root command with args, returning combined output.
// Always drains UI push and closes the store (mirrors Execute's error-path
// cleanup — Cobra skips PersistentPostRunE when RunE fails).
//
// The registered command tree is process-global (init → register); pflag
// Changed bits and string values persist across Execute calls. Reset them so
// sequential runRoot invocations in one test (and across tests in a package)
// do not trip mutual-exclusion checks on leftover flags (sty_40e5a305).
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	resetFlagState(root)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := executeRoot(root)
	return buf.String(), err
}

// executeRoot runs root in-process and leaves package verb's wiring as it found
// it. The command wires the verb layer to the app it opens (stores, directories,
// per-run resolvers); closeAppForCmd joins the app's background work and closes
// its stores, and only then is the prior wiring put back, so nothing the run
// started can read the restored wiring and the next run or test does not
// resolve paths into this one's temp dir. Every in-process run goes through here.
func executeRoot(root *cobra.Command) error {
	restore, _ := verb.SnapshotWiring()
	defer restore()
	// Flags are bound to package-level variables the command tree shares across
	// runs (hookHarnessFlag, hookNoWakeFlag, ...); a value parsed here would
	// otherwise steer the next test that calls a hook handler directly.
	defer resetFlagState(root)
	c, err := root.ExecuteC()
	if c != nil {
		closeAppForCmd(c)
	}
	return err
}

// withVerbWiring restores package verb's wiring to what it was when the test
// started. A test that calls verb.Set*, verb.Clear* or verb.Add* calls it first
// (TestVerbWiringCallsAreGuarded fails otherwise) — never a hand-written reset
// to nil or zero, which would clobber a production default.
func withVerbWiring(t *testing.T) {
	t.Helper()
	restore, _ := verb.SnapshotWiring()
	t.Cleanup(restore)
}

// resetFlagState clears Changed and restores DefValue on every flag in the
// command tree so a prior Execute cannot poison the next one. It also drops any
// output writer or context a test set directly on a registered command: a
// command's own writer beats its parent's, so a stale SetOut left by an earlier
// test (or an earlier -count iteration) would swallow this run's output.
func resetFlagState(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	// A test that calls SetOut/SetErr/SetIn on a registered (process-global)
	// command pins that writer on it for good; the next run's root buffer is then
	// bypassed and the command prints into the earlier test's buffer.
	cmd.SetOut(nil)
	cmd.SetErr(nil)
	cmd.SetIn(nil)
	cmd.SetContext(nil)
	cmd.Flags().VisitAll(resetFlag)
	cmd.PersistentFlags().VisitAll(resetFlag)
	for _, c := range cmd.Commands() {
		resetFlagState(c)
	}
}

// resetFlag returns one flag to its declared default. A slice flag cannot be
// reset by Set(DefValue): after its first Set every later Set appends, so the
// tags of one run's --tags would ride into the next run's story.
func resetFlag(f *pflag.Flag) {
	f.Changed = false
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		var def []string
		if d := strings.Trim(f.DefValue, "[]"); d != "" {
			def = strings.Split(d, ",")
		}
		_ = sv.Replace(def)
		return
	}
	_ = f.Value.Set(f.DefValue)
}

// tempRepo creates a repo with .satelle/satelle.toml and points SATELLE_CONFIG
// at it, so config resolution lands there without a process-global chdir.
// Isolates SATELLE_HOME so the home-keyed runtime plane (sty_4660bbe1) does not
// write into the developer's real ~/.satelle during tests.
// Also scrubs SATELLE_SESSION: fixtures acquire seats without a session stamp,
// and an ambient value — inherited whenever the suite runs as a child of an
// engaged `satelle story set` (the build-unit-check gate does exactly that) —
// would make those seats read as foreign-session. Tests that need a session
// identity set it themselves after the fixture.
func tempRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv(config.SessionEnv, "")
	_ = os.Unsetenv(config.SessionEnv)
	if strings.TrimSpace(os.Getenv("SATELLE_SERVER_ENDPOINT")) == "" {
		t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	}
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(satelleDir, "satelle.toml")
	// Explicit gate_create = false: hermetic fixtures that create stories must
	// not trip the ungated-create notice (stderr advisory that would break
	// combined out+err JSON parsers). Pre-seed / gated cases rewrite this file.
	if err := os.WriteFile(cfgPath, []byte("[review]\ngate_create = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An initialized repo must carry its agents layer — store-backed commands
	// refuse without it (requireAgents, sty_d0d6bb67). init always seeds it; the
	// hand-scaffolded test repo does the same.
	if err := os.WriteFile(filepath.Join(satelleDir, "agents.toml"), []byte("[executor]\nharness = \"in-loop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SATELLE_CONFIG", cfgPath)
	return repo
}

// runtimeDBPath returns the resolved satelle.db path for the current SATELLE_CONFIG
// / SATELLE_HOME env (home-keyed by default).
func runtimeDBPath(t *testing.T) string {
	t.Helper()
	cfg, cfgPath, err := config.Load("")
	if err != nil && err != config.ErrNotFound {
		t.Fatal(err)
	}
	repoRoot := "."
	if cfgPath != "" {
		repoRoot = config.RepoRootFromConfigPath(cfgPath)
	}
	return cfg.ResolveDB(repoRoot)
}

func TestVersionDoesNotCreateDB(t *testing.T) {
	repo := tempRepo(t)
	out, err := runRoot(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(out, "satelle ") {
		t.Errorf("version output = %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".satelle", "satelle.db")); statErr == nil {
		t.Error("version created a database — bootstrap should not open the store")
	}
}
