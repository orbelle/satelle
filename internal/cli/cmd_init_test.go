package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/structure"
)

// isolateUserHome pins HOME and SATELLE_HOME to a disposable dir for this test
// so init's Grok-compat write (~/.grok/config.toml) and the home-keyed runtime
// plane cannot touch the developer's real config/home (sty_24b32127,
// sty_c36c211f). Idempotent within a test.
func isolateUserHome(t *testing.T) {
	t.Helper()
	if os.Getenv("SATELLE_INIT_TEST_HOME") != "" {
		// Still enforce SATELLE_HOME — GlobalDir panics under test without it.
		if strings.TrimSpace(os.Getenv("SATELLE_HOME")) == "" {
			t.Setenv("SATELLE_HOME", t.TempDir())
		}
		// An exported XDG_CONFIG_HOME would point the credentials file at the
		// operator's real ~/.config (sty_18403814).
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		return
	}
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("SATELLE_HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("SATELLE_INIT_TEST_HOME", h)
}

// runInitTest is runInit with HOME/SATELLE_HOME isolated (see isolateUserHome).
// Registers the repo in the local workspace registry by default (sty_3bdbdc38).
func runInitTest(t *testing.T, out io.Writer, repo string) error {
	t.Helper()
	isolateUserHome(t)
	return runInit(out, repo, false, nil)
}

// TestRunInitSeedsActiveEditExemptPaths proves the scaffold satelle.toml ships an
// ACTIVE (uncommented) [gate] edit_exempt_paths seeded with .satelle/ and
// .gitignore (sty_8c3d345c / sty_f115e6bf). Exemption is config, not code — a
// fresh repo keeps authored substrate and satelle-managed .gitignore editable
// via this seeded config rather than a hardcoded case in the binary.
func TestRunInitSeedsActiveEditExemptPaths(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repo, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatalf("read scaffold satelle.toml: %v", err)
	}
	tomlSrc := string(body)
	// The [gate] table and edit_exempt_paths must be ACTIVE (no leading '#') and
	// seed .satelle/ plus the footprint the binary itself deploys.
	for _, want := range []string{
		"\n[gate]\n",
		"edit_exempt_paths = " + defaultEditExemptTOML(),
		"edit_exempt_globs = " + defaultEditExemptGlobsTOML(),
		"allow_outside_tree_edits",
	} {
		if !strings.Contains(tomlSrc, want) {
			t.Errorf("scaffold satelle.toml missing active %q:\n%s", want, tomlSrc)
		}
	}
	// Parse it to confirm the seeded values actually resolve as exempt prefixes.
	var cfg config.Config
	if _, err := toml.Decode(tomlSrc, &cfg); err != nil {
		t.Fatalf("scaffold satelle.toml does not parse: %v", err)
	}
	got := cfg.ResolveEditExemptPaths(repo)
	want := defaultEditExemptPaths()
	if len(got) != len(want) {
		t.Fatalf("ResolveEditExemptPaths = %v, want %d entries (%v)", got, len(want), want)
	}
	for i, w := range want {
		gotEnd := strings.TrimSuffix(got[i], string(filepath.Separator))
		wantEnd := strings.TrimSuffix(w, "/")
		if !strings.HasSuffix(gotEnd, wantEnd) {
			t.Errorf("ResolveEditExemptPaths[%d] = %q, want ending %q", i, got[i], w)
		}
	}
	if globs := cfg.ResolveEditExemptGlobs(); strings.Join(globs, ",") != strings.Join(managedEditExemptGlobs, ",") {
		t.Errorf("ResolveEditExemptGlobs = %v, want %v", globs, managedEditExemptGlobs)
	}
}

// TestInstallAliasRemoved: satelle install is no longer an alias of init
// (breaking surface). The retiredNames guard names satelle init.
func TestInstallAliasRemoved(t *testing.T) {
	root := NewRootCmd()
	initCmd, _, err := root.Find([]string{"init"})
	if err != nil {
		t.Fatalf("find init: %v", err)
	}
	for _, a := range initCmd.Aliases {
		if a == "install" {
			t.Fatalf("install alias must be removed, still on init: %v", initCmd.Aliases)
		}
	}
	if _, _, err := root.Find([]string{"install"}); err == nil {
		t.Fatal("install must not resolve as a command")
	}
	msg := retiredNameMessage([]string{"install"})
	if !strings.Contains(msg, "satelle init") {
		t.Fatalf("retired message: %q", msg)
	}
}

// TestRunInitFailsValidationOnBrokenSubstrate: init on a repo whose authored
// substrate does not validate exits non-zero, naming the failures — the runtime
// refuses broken configuration, so init must not report success over it
// (sty_d0d6bb67).
func TestRunInitFailsValidationOnBrokenSubstrate(t *testing.T) {
	repo := t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A structurally broken authored workflow (missing type/description/scope/DOT).
	if err := os.WriteFile(filepath.Join(wfDir, "broken.md"), []byte("---\nname: broken\n---\n# broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := runInitTest(t, &out, repo)
	if err == nil {
		t.Fatal("init must exit non-zero when the deployed system fails validation")
	}
	if !strings.Contains(err.Error(), "failed validation") {
		t.Errorf("error should say the deployed system failed validation: %v", err)
	}
	// init prints doctor's findings, so each FAIL carries the stable id as well as
	// the artifact (sty_e9da28e2).
	if !strings.Contains(out.String(), "FAIL  [workflow.structure] workflows/broken") {
		t.Errorf("report should name the finding id and the failing artifact:\n%s", out.String())
	}
}

// sty_642275eb: [sync] hold_stale_after is retired. init on an empty repo never
// writes it, and init over an existing config removes it, leaving every other
// line (comments and the other [sync] keys) as it was; migrate does the same.
func TestInitEmptyRepoWritesNoRetiredSyncKey(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "hold_stale_after") {
		t.Errorf("init wrote the retired key:\n%s", got)
	}
}

// checkScript extracts the self-contained ```check block via structure.CheckFence
// (single extractor — sty_6830e78e).
func checkScript(t *testing.T, skillBody string) string {
	t.Helper()
	s := structure.CheckFence(skillBody)
	if s == "" {
		t.Fatal("task-validate-before skill carries no ```check block")
	}
	return s
}

// defaultSolutionSkills is every gate skill the shipped default route names —
// the set a fresh repo must resolve so nothing dangles. They come off the route
// grammar: the wildcard lane's intent / close triad, the park and cancel gates,
// the always-on estimate check and step summary, the task section's two validate
// gates, and the [meta] lifecycle hooks (amend_review — sty_5c768dd3), which name
// a gate skill without being an edge.
var defaultSolutionSkills = []string{
	"satelle-estimate-actual-review",
	"satelle-instruction-change-review",
	"satelle-instruction-change-trigger",
	"satelle-story-amend-review",
	"satelle-step-summary",
	"satelle-story-blocked-review",
	"satelle-story-cancel-review",
	"satelle-story-done-review",
	"satelle-story-intent-review",
	"satelle-story-scope-review",
	"satelle-workflow-change-review",
	"satelle-task-validate-before-review",
	"satelle-task-validate-after-review",
}

func TestEnsureGitignoreAppendsOnce(t *testing.T) {
	repo := t.TempDir()
	giPath := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(giPath, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := ensureGitignore(repo)
	if err != nil || len(added) == 0 {
		t.Fatalf("first ensureGitignore: added=%v err=%v", added, err)
	}
	added2, _ := ensureGitignore(repo)
	if len(added2) != 0 {
		t.Errorf("second ensureGitignore should be a no-op, added %v", added2)
	}
	gi, _ := os.ReadFile(giPath)
	if !strings.Contains(string(gi), "node_modules/") {
		t.Error("existing .gitignore content lost")
	}
	if strings.Count(string(gi), gitignoreMarker) != 1 {
		t.Error("managed block appended more than once")
	}
	// Fresh append must use the home-keyed form (no runtime db ignore entries).
	if strings.Contains(string(gi), ".satelle/satelle.db") {
		t.Error("gitignore must not ignore .satelle/satelle.db (home-keyed)")
	}
	// sty_552d2d87: managed block must not ignore the repo agents layer —
	// committed substrate when process is tracked; execution detail is catalog/local.
	// Comments may mention agents.toml; only non-comment lines are ignore rules.
	for _, line := range strings.Split(string(gi), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.Contains(trim, "agents.toml") {
			t.Errorf("managed gitignore must not ignore agents.toml (repo agents is committed substrate); got rule %q", trim)
		}
	}
}

// TestRunInitLeavesSettledGitignoreUntouched (sty_dbc0be85 AC1): re-running
// init on a repo whose .gitignore already settles everything satelle needs — an
// older-wording managed block here — leaves the file byte-identical and its
// mtime unchanged, and reports it unchanged.
func TestRunInitLeavesSettledGitignoreUntouched(t *testing.T) {
	repo := t.TempDir()
	// Pre-seed a minimal satelle tree so init is a re-run, not a first scaffold
	// that also creates agents/db. Stale managed block is the subject under test.
	if err := os.MkdirAll(filepath.Join(repo, ".satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".satelle", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "workflows", "agents.toml"), []byte("[executor]\nharness = \"in-loop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := "node_modules/\n# >>> satelle (managed) >>>\n# an older wording of the header\n" +
		strings.Join(satelleTrackedSubstrateIgnores, "\n") +
		"\n.satelle/satelle.db\nsty_*_body.md\nsty_*_ac.md\n# <<< satelle (managed) <<<\n# keep-me\n"
	giPath := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(giPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(giPath, past, past); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gi) != old {
		t.Errorf(".gitignore changed on init re-run:\n%s", gi)
	}
	fi, err := os.Stat(giPath)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Errorf(".gitignore mtime changed: %v, want %v", fi.ModTime(), past)
	}
	if !strings.Contains(out.String(), "= .gitignore") {
		t.Errorf("init should report .gitignore unchanged:\n%s", out.String())
	}
}

// TestInitLongHelpNamesHomeKeyedRuntime (sty_87c8a69c AC3).
func TestInitLongHelpNamesHomeKeyedRuntime(t *testing.T) {
	// The cobra Long is in package init; assert via the command surface.
	root := NewRootCmd()
	var initCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "init" {
			initCmd = c
			break
		}
	}
	if initCmd == nil {
		t.Fatal("init command missing")
	}
	long := initCmd.Long
	if !strings.Contains(long, "~/.satelle/<repo-key>") && !strings.Contains(long, "home-keyed") {
		t.Errorf("init help must name home-keyed runtime, got:\n%s", long)
	}
	// Must not claim the DB lives at .satelle/satelle.db as the default layout.
	if strings.Contains(long, "database at .satelle/satelle.db") {
		t.Errorf("init help still claims in-repo satelle.db:\n%s", long)
	}
}

func TestEnsureClaudeHooksIdempotent(t *testing.T) {
	repo := t.TempDir()
	created, _, _, err := ensureClaudeHooks(repo)
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v, want created", created, err)
	}
	path := filepath.Join(repo, ".claude", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	for _, want := range []string{
		renderHookCommand(repo, "claude", "gate"),
		renderHookCommand(repo, "claude", "commitgate"),
		"PATH=$HOME/.local/bin:$PATH satelle hook prompt",
		"PATH=$HOME/.local/bin:$PATH satelle hook stopcheck",
		"UserPromptSubmit",
		"Stop",
		"satelle hook context",
		"Edit|Write",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("settings.json missing %q", want)
		}
	}
	if strings.Contains(string(b), "|| exit 2") {
		t.Errorf("settings must not use bare '|| exit 2'")
	}
	if strings.Contains(string(b), "sh -c ") {
		t.Errorf("settings must not use inline sh -c PreToolUse")
	}
	if _, err := os.Stat(filepath.Join(repo, ".satelle", "hooks", "satelle-hook.sh")); err != nil {
		t.Errorf("gate script not written: %v", err)
	}
	// An existing file that LACKS the reinforcement hooks is HEALED, not left
	// ungated (sty_949e8739): prompt/stopcheck are appended and other keys are
	// preserved — otherwise a repo initialized before the reinforcement hooks
	// shipped would never gain them, re-opening the bypass.
	if err := os.WriteFile(path, []byte("{\"custom\":true}"), 0o644); err != nil {
		t.Fatal(err)
	}
	created2, updated2, _, err := ensureClaudeHooks(repo)
	if err != nil || created2 {
		t.Fatalf("second call: created=%v err=%v, want not created", created2, err)
	}
	if len(updated2) == 0 {
		t.Errorf("heal: expected reinforcement hooks to be reported as added")
	}
	b2, _ := os.ReadFile(path)
	for _, want := range []string{`"custom": true`, "satelle hook prompt", "satelle hook stopcheck"} {
		if !strings.Contains(string(b2), want) {
			t.Errorf("heal: settings.json missing %q after heal:\n%s", want, b2)
		}
	}
	// Idempotent: a THIRD call adds nothing and rewrites nothing (already healed).
	before, _ := os.ReadFile(path)
	created3, updated3, _, err := ensureClaudeHooks(repo)
	after, _ := os.ReadFile(path)
	if err != nil || created3 || len(updated3) != 0 || string(before) != string(after) {
		t.Fatalf("third call not idempotent: created=%v updated=%v err=%v changed=%v",
			created3, updated3, err, string(before) != string(after))
	}
}

// TestInitAgentsLayerValidatesZeroWarnings is the sty_5f1d7b2e regression guard:
// a fresh init deploys an agents.toml that agentvalidate accepts with ZERO
// warnings and ZERO problems. Any future scaffold-vs-validator drift (missing
// role=, role/path mismatch, in-loop reviewer, …) fails this test instead of
// shipping WARN lines to first-run users.
func TestInitAgentsLayerValidatesZeroWarnings(t *testing.T) {
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	// Init output itself must not emit agents-layer WARN lines (AC1 surface).
	//
	// binary.missing is excluded deliberately: it reports that the machine has no
	// provider CLI on PATH, which is an ENVIRONMENT fact, not a defect in the
	// agents layer this test guards. CI has no agent CLI installed, and a repo is
	// legitimately initialised before one exists (sty_e9da28e2).
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, health.IDBinaryMissing) {
			continue
		}
		if strings.Contains(line, "WARN") && strings.Contains(line, "agents.toml") {
			t.Errorf("fresh init emitted agents-layer WARN:\n%s", line)
		}
	}
	dataDir := filepath.Join(repo, config.DefaultDataDir)
	agents, err := config.LoadAgents(dataDir)
	if err != nil {
		t.Fatalf("LoadAgents after init: %v", err)
	}
	report := agentvalidate.Validate(agents, deployedVars(dataDir), deployedWorkflowDocs(dataDir))
	if len(report.Warnings) > 0 || len(report.Problems) > 0 {
		t.Fatalf("init-deployed agents layer must validate with zero warnings/problems:\n  warnings=%v\n  problems=%v",
			report.Warnings, report.Problems)
	}
	// AC4: re-init leaves an existing agents.toml untouched.
	path := filepath.Join(dataDir, config.AgentsConfigDir, config.AgentsConfigName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("re-init clobbered existing agents.toml")
	}
}

// TestRunInitRegistersWorkspace (sty_3bdbdc38 AC1): a fresh init leaves the repo
// in the local workspace registry that `satelle workspace list` reads.
func TestRunInitRegistersWorkspace(t *testing.T) {
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("workspace registry missing %s: %v", abs, gc.Workspace.Repos)
	}
	if !strings.Contains(out.String(), "registered") {
		t.Errorf("init output missing registered line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "workspace: member") {
		t.Errorf("init output missing membership line:\n%s", out.String())
	}
}

// TestRunInitWorkspaceMembershipLines (sty_805bee9c AC3): member on init/re-init;
// not-member when --no-workspace, naming the join verb.
func TestRunInitWorkspaceMembershipLines(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	var out1 strings.Builder
	if err := runInitTest(t, &out1, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out1.String())
	}
	if !strings.Contains(out1.String(), "workspace: member") {
		t.Fatalf("fresh init missing member line:\n%s", out1.String())
	}
	var out2 strings.Builder
	if err := runInitTest(t, &out2, repo); err != nil {
		t.Fatalf("re-init: %v\n%s", err, out2.String())
	}
	if !strings.Contains(out2.String(), "workspace: member") {
		t.Fatalf("re-init missing member line:\n%s", out2.String())
	}

	isolateUserHome(t)
	repo2 := t.TempDir()
	var out3 strings.Builder
	if err := runInit(&out3, repo2, true, nil); err != nil {
		t.Fatalf("no-workspace init: %v\n%s", err, out3.String())
	}
	if !strings.Contains(out3.String(), "workspace: not-member") {
		t.Fatalf("opt-out missing not-member:\n%s", out3.String())
	}
	if !strings.Contains(out3.String(), "satelle workspace add") {
		t.Fatalf("opt-out must name join verb:\n%s", out3.String())
	}
}

// TestRunInitWorkspaceIdempotent (AC2): re-init does not duplicate and does not fail.
func TestRunInitWorkspaceIdempotent(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("re-init: %v\n%s", err, out.String())
	}
	abs, _ := filepath.Abs(repo)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 registry entry for %s, got %d in %v", abs, n, gc.Workspace.Repos)
	}
	if !strings.Contains(out.String(), "already registered") {
		t.Errorf("re-init output missing already-registered line:\n%s", out.String())
	}
	if strings.Contains(out.String(), "  + workspace registry") {
		t.Errorf("re-init must not re-add (no + line):\n%s", out.String())
	}
}

// TestRunInitNoWorkspaceOptOut (AC3): --no-workspace skips registration.
func TestRunInitNoWorkspaceOptOut(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	var out strings.Builder
	if err := runInit(&out, repo, true, nil); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	abs, _ := filepath.Abs(repo)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			t.Fatalf("opt-out still registered %s in %v", abs, gc.Workspace.Repos)
		}
	}
	if !strings.Contains(out.String(), "skipped: --no-workspace") {
		t.Errorf("output missing skip line:\n%s", out.String())
	}
}

// TestRunInitWorkspaceWriteFailureNonFatal (AC4): unwritable global config warns
// but does not fail init. Runtime dir must still be creatable (home-keyed DB);
// only SaveGlobal's config.toml write is blocked (sty_4660bbe1).
func TestRunInitWorkspaceWriteFailureNonFatal(t *testing.T) {
	isolateUserHome(t)
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	// Make config.toml a directory so SaveGlobal's WriteFile fails while
	// MkdirAll(home/<repo-key>) for the runtime plane still succeeds.
	if err := os.MkdirAll(filepath.Join(home, config.GlobalConfigName), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInit(&out, repo, false, nil); err != nil {
		t.Fatalf("init must succeed despite unwritable global config: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "workspace registry") || !strings.Contains(out.String(), "!") {
		// Accept either "!" warn prefix or "skipped" wording.
		if !strings.Contains(out.String(), "registration skipped") && !strings.Contains(out.String(), "workspace registry (skipped") {
			t.Errorf("expected non-fatal workspace warning:\n%s", out.String())
		}
	}
}
