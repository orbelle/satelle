package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/testutil"
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

func TestRunInitScaffolds(t *testing.T) {
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	// Config + empty authored dirs under the repo; DB on the home-keyed runtime
	// plane (sty_4660bbe1). Virtual sparse defaults (sty_29e5a9a5): no unedited
	// default markdown is seeded.
	for _, rel := range []string{
		".satelle/satelle.toml",
		".satelle/workflows/agents.toml",
		".satelle/documents/README.md",
		".satelle/workflows/README.md",
		".satelle/principles/README.md",
		".satelle/skills/README.md",
		".satelle/tasks/README.md",
		".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	// sty_552d2d87: seeded agents.toml carries the committed-substrate posture.
	agentsBody, err := os.ReadFile(filepath.Join(repo, ".satelle", "workflows", "agents.toml"))
	if err != nil {
		t.Fatalf("read agents.toml: %v", err)
	}
	if !strings.Contains(string(agentsBody), "COMMITTED SUBSTRATE") {
		t.Errorf("scaffold agents.toml missing COMMITTED SUBSTRATE posture comment:\n%s", agentsBody)
	}
	if !strings.Contains(string(agentsBody), "satelle help global-agents") {
		t.Errorf("scaffold agents.toml should point at satelle help global-agents:\n%s", agentsBody)
	}

	// No unedited default seeds.
	for _, rel := range []string{
		".satelle/skills/satelle-step-summary.md",
		".satelle/workflows/satelle-baseline-workflow.md",
		".satelle/principles/satelle-agent-goals.md",
		".satelle/tasks/tsk_example1.md",
	} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err == nil {
			t.Errorf("init must not seed unedited default %s (sty_29e5a9a5)", rel)
		}
	}
	// Default tasks are virtual too: init seeds no tsk_*.md (sty_097833e8).
	if _, err := os.Stat(filepath.Join(repo, ".satelle/tasks/tsk_substrate-audit.md")); err == nil {
		t.Error("init must not seed tasks/tsk_substrate-audit.md — default tasks are virtual")
	}
	// Runtime DB is under ~/.satelle/<repo-key>/ (HOME isolated by runInitTest).
	homeDB := filepath.Join(config.GlobalDir(), config.RepoKey(repo), config.DefaultDBName)
	if _, err := os.Stat(homeDB); err != nil {
		t.Errorf("missing home-keyed db %s: %v", homeDB, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".satelle", config.DefaultDBName)); err == nil {
		t.Error("init must not write satelle.db under the repo")
	}
	// The removed .satelle/stories mirror must NOT be recreated (sty_746a0c98).
	if _, err := os.Stat(filepath.Join(repo, ".satelle/stories")); err == nil {
		t.Error("init must not scaffold .satelle/stories — the markdown mirror was removed")
	}

	// gitignore keeps local.toml + pinned binary; runtime paths left the repo (AC4).
	gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if strings.Contains(string(gi), ".satelle/satelle.db") {
		t.Error("gitignore must not list satelle.db — runtime is home-keyed")
	}
	// sty_dbc0be85: with no setting for .satelle, the default is one `.satelle/`
	// rule — it covers the logs pointer, local.toml and the pinned binary, so
	// the finer local-state lines are added only when .satelle/ is not ignored.
	if !gitignoreTextIgnoresSatelleDir(string(gi)) {
		t.Errorf("gitignore must ignore .satelle/ on a fresh init:\n%s", gi)
	}
	if strings.Contains(string(gi), ".satelle/logs/") {
		t.Error("gitignore must not list .satelle/logs/ (trailing slash misses the symlink)")
	}
	if strings.Contains(string(gi), "\n.satelle/satelle.toml\n") {
		t.Error("gitignore should not ignore the committed toml")
	}

	// Report shows the home-keyed db creation.
	if !strings.Contains(out.String(), homeDB) {
		t.Errorf("report missing home-keyed db path %s:\n%s", homeDB, out.String())
	}

	// init ends by PROVING the deployment green (sty_d0d6bb67): the validation
	// pass runs and the fresh seeded system validates.
	for _, want := range []string{"Validating the deployed system:", "PASS  deployed system validates green"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
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

// TestRunInitAdvisorySkillsAreVirtual: advisory skills resolve from the binary
// without seeding (sty_29e5a9a5), even beside an authored workflow set.
func TestRunInitAdvisorySkillsAreVirtual(t *testing.T) {
	repo := t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	p := filepath.Join(repo, ".satelle", "skills", "satelle-workflow-advisor.md")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("advisor skill must not be seeded onto disk")
	}
	body, ok := embeddedDefault("skills", "satelle-workflow-advisor")
	if !ok {
		t.Fatal("advisor skill missing from EmbeddedDefaults")
	}
	if problems := structure.Doc("skills", "satelle-workflow-advisor", body, nil); len(problems) > 0 {
		t.Errorf("embedded advisor skill fails its structure check: %v", problems)
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

func TestRunInitIdempotent(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatal(err)
	}
	// Capture a user edit to the toml; a second init must not clobber it.
	tomlPath := filepath.Join(repo, ".satelle", "satelle.toml")
	marker := "\nlog_level = \"debug\"\n"
	orig, _ := os.ReadFile(tomlPath)
	if err := os.WriteFile(tomlPath, append(orig, []byte(marker)...), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	// Everything reported as already present.
	if strings.Contains(out.String(), "  + ") {
		t.Errorf("second init created something:\n%s", out.String())
	}
	// The user edit survived.
	after, _ := os.ReadFile(tomlPath)
	if !strings.Contains(string(after), `log_level = "debug"`) {
		t.Error("second init clobbered the user's toml edit")
	}

	// An authored task the operator wrote survives re-init (never clobbered),
	// even though init no longer seeds any example task (sty_04ec1fe6).
	taskPath := filepath.Join(repo, ".satelle", "tasks", "tsk_mine.md")
	authored := "---\nid: tsk_mine\ntype: task\nstatus: in_progress\n---\n\n# Mine\n\nACTION; VERIFICATION.\n"
	if err := os.WriteFile(taskPath, []byte(authored), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(taskPath); string(got) != authored {
		t.Errorf("re-init clobbered the authored task:\n%s", got)
	}
}

// TestRunInitFoldsLeftoverHosted: collector-sdk shape — [sync] plus leftover
// [hosted] project — becomes the simple [sync] view (sty_5eb1bb8a), and its
// removed scope key is dropped on the way (sty_b336c2b0).
func TestRunInitFoldsLeftoverHosted(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(repo, ".satelle", "satelle.toml")
	body := `[sync]
all = "personal"

[hosted]
project = "solidsafe-collector-sdk-go"
`
	if err := os.WriteFile(tomlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "[hosted]") {
		t.Fatalf("init left [hosted]:\n%s", s)
	}
	if !strings.Contains(s, `project = "solidsafe-collector-sdk-go"`) {
		t.Fatalf("init did not fold project onto [sync]:\n%s", s)
	}
	if strings.Contains(s, `all = `) {
		t.Fatalf("init left the removed [sync] all key:\n%s", s)
	}
	if !strings.Contains(out.String(), "folded into [sync]") {
		t.Errorf("init should report the fold:\n%s", out.String())
	}
}

func TestHealStaleAfterSeedsMissingKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "satelle.toml")
	if err := os.WriteFile(path, []byte("[review]\ngate_create = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := healStaleAfter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected heal to insert stale_after")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `stale_after = "24h"`) {
		t.Fatalf("missing stale_after:\n%s", got)
	}
	changed, err = healStaleAfter(dir)
	if err != nil || changed {
		t.Fatalf("second heal must not clobber: changed=%v err=%v", changed, err)
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

func TestInitRemovesRetiredHoldStaleAfter(t *testing.T) {
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "satelle.toml")
	before := "# keep me\n[sync]\nstale_after = \"24h\"\nhold_stale_after = \"30m\"\nhold_unpushed = false\n\n[review]\ngate_create = false\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "hold_stale_after") {
		t.Fatalf("init left the retired key in place:\n%s", got)
	}
	for _, keep := range []string{"# keep me\n", "stale_after = \"24h\"\n", "hold_unpushed = false\n", "[review]\ngate_create = false\n"} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("init disturbed %q:\n%s", keep, got)
		}
	}
	if !strings.Contains(out.String(), "removed retired [sync] keys") {
		t.Errorf("init output does not report the removal:\n%s", out.String())
	}
}

func TestMigrateRemovesRetiredHoldStaleAfter(t *testing.T) {
	testutil.IsolateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "satelle.toml")
	before := "# keep me\n[sync]\nstale_after = \"24h\"\nhold_stale_after = \"30m\"\nhold_unpushed = false\n\n[review]\ngate_create = false\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	p := planMigrate(config.Config{}, t.TempDir(), dir)
	if !p.RetiredSyncKeyRemove || p.StaleAfter {
		t.Fatalf("plan = StaleAfter %v RetiredSyncKeyRemove %v; want only the retired key to remove", p.StaleAfter, p.RetiredSyncKeyRemove)
	}
	if p.empty() {
		t.Fatal("plan must not be empty while the retired key is set")
	}

	changed, err := removeRetiredSyncKey(dir)
	if err != nil || !changed {
		t.Fatalf("expected the key removed: changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(path)
	want := "# keep me\n[sync]\nstale_after = \"24h\"\nhold_unpushed = false\n\n[review]\ngate_create = false\n"
	if string(got) != want {
		t.Fatalf("config after migrate:\n%s\nwant:\n%s", got, want)
	}
	if changed, err = removeRetiredSyncKey(dir); err != nil || changed {
		t.Fatalf("second run must change nothing: changed=%v err=%v", changed, err)
	}
	if p := planMigrate(config.Config{}, t.TempDir(), dir); p.RetiredSyncKeyRemove {
		t.Error("the plan still asks to remove a key that is gone")
	}
}

func TestPlanMigrateIncludesMissingStaleAfter(t *testing.T) {
	testutil.IsolateHome(t)
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "satelle.toml"), []byte("[review]\ngate_create = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := planMigrate(config.Config{}, t.TempDir(), dir)
	if !p.StaleAfter {
		t.Fatal("missing stale_after must appear on the migrate plan")
	}
	if p.empty() {
		t.Fatal("plan must not be empty when stale_after is missing")
	}
}

// TestRunInitAuditTaskIsVirtualAndRerunnable asserts the embedded substrate-audit
// task is NOT seeded onto disk (it comes from the binary, so it never syncs as if
// authored — sty_097833e8), is a well-formed re-runnable task at done
// (sty_d4360e90), and that the begin-run gate accepts a new run under it by
// resolving it from the binary.
func TestRunInitAuditTaskIsVirtualAndRerunnable(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	dataDir := filepath.Join(repo, ".satelle")
	if fileExists(filepath.Join(dataDir, "tasks", "tsk_substrate-audit.md")) {
		t.Error("init seeded tasks/tsk_substrate-audit.md — default tasks are virtual")
	}
	body, ok := embeddedDefault("tasks", "tsk_substrate-audit")
	if !ok {
		t.Fatal("embedded tsk_substrate-audit missing")
	}
	if problems := structure.CheckTask(body); len(problems) > 0 {
		t.Errorf("embedded audit task fails CheckTask: %v", problems)
	}
	if !strings.Contains(body, "\nstatus: done\n") {
		t.Errorf("embedded audit task must sit at status: done, got:\n%s", body)
	}
	// Workflows/skills still virtual.
	if fileExists(filepath.Join(dataDir, "workflows", "done.md")) {
		t.Error("workflows must not be seeded")
	}

	// The audit task's KIND resolves to the route's own task section — not to the
	// wildcard lane (sty_3795e7f6).
	if spec := embeddedRouteFor(t, "task"); !spec.HasEdge("backlog", "in_progress") ||
		!hasGate(spec, "backlog", "in_progress", "satelle-task-validate-before-review") {
		t.Errorf("a task does not resolve to the route's task section: %+v", spec.Transitions)
	}

	skillBody, ok := embeddedDefault("skills", "satelle-task-validate-before-review")
	if !ok {
		t.Fatal("embedded task-validate-before skill missing")
	}
	payload := `{"story":{"id":"exe_test0001","kind":"execution","title":"audit run","status":"backlog","parent_id":"tsk_substrate-audit","tags":[],"created_at":"2026-07-08T00:00:00Z","updated_at":"2026-07-08T00:00:00Z"},"from":"backlog","to":"in_progress","review_skill":"satelle-task-validate-before-review"}`
	// A stand-in `satelle` on PATH validates exactly the default tasks, so the
	// gate's virtual branch is exercised without depending on an installed binary.
	bin := t.TempDir()
	stub := "#!/bin/sh\n[ \"$1 $2 $3\" = \"task validate tsk_substrate-audit\" ] && echo 'PASS  tasks/tsk_substrate-audit (embedded default)'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "satelle"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := func(payload string) (string, error) {
		cmd := exec.Command("sh", "-c", checkScript(t, skillBody))
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(payload)
		var gate bytes.Buffer
		cmd.Stdout = &gate
		cmd.Stderr = &gate
		err := cmd.Run()
		return gate.String(), err
	}
	if out, err := run(payload); err != nil {
		t.Errorf("begin-run gate rejected a new run under the done audit task: %v\n%s", err, out)
	}
	// A parent that is neither a header file nor a default task is still refused.
	if out, err := run(strings.ReplaceAll(payload, "tsk_substrate-audit", "tsk_nope")); err == nil {
		t.Errorf("begin-run gate accepted a run under a task with no header and no default:\n%s", out)
	}
}

// TestValidateEmbeddedTaskNamesOnlyShippedDefaults: with no file on disk, a named
// task check passes a default task from the binary's own copy and says nothing for
// any other name — the distinction the begin-run gate relies on.
func TestValidateEmbeddedTaskNamesOnlyShippedDefaults(t *testing.T) {
	var out strings.Builder
	if v, f := validateEmbeddedTask(&out, "tsk_substrate-audit"); v != 1 || f != 0 {
		t.Errorf("default task: validated=%d failed=%d\n%s", v, f, out.String())
	}
	if !strings.Contains(out.String(), "PASS  tasks/tsk_substrate-audit (embedded default)") {
		t.Errorf("missing the PASS line the gate reads:\n%s", out.String())
	}
	out.Reset()
	if v, f := validateEmbeddedTask(&out, "tsk_not_shipped"); v != 0 || f != 0 || out.Len() != 0 {
		t.Errorf("a task the binary does not ship produced output: validated=%d failed=%d %q", v, f, out.String())
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

// TestRunInitVirtualDefaultSolution asserts a fresh init does NOT seed the
// default solution onto disk (sty_29e5a9a5) yet validates green — defaults resolve
// virtually and skill refs resolve against the embedded set.
func TestRunInitVirtualDefaultSolution(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	dataDir := filepath.Join(repo, ".satelle")

	for _, wf := range defaultSolutionWorkflows {
		if fileExists(filepath.Join(dataDir, "workflows", wf+".md")) {
			t.Errorf("init must not seed workflows/%s.md (virtual defaults)", wf)
		}
	}
	for _, sk := range defaultSolutionSkills {
		if fileExists(filepath.Join(dataDir, "skills", sk+".md")) {
			t.Errorf("init must not seed skills/%s.md (virtual defaults)", sk)
		}
	}

	// Embedded defaults are structure-conformant and skills resolve virtually.
	resolve := skillResolves(dataDir)
	var docs []docindex.Doc
	for _, wf := range defaultSolutionWorkflows {
		body, ok := embeddedDefault("workflows", wf)
		if !ok {
			t.Fatalf("embedded workflow %s missing", wf)
		}
		for _, p := range structure.Doc("workflows", wf, body, resolve) {
			t.Errorf("embedded workflows/%s: %s", wf, p)
		}
		docs = append(docs, docindex.Doc{Name: wf, Body: body})
	}
	for _, p := range agentstep.WorkflowConsistency(docs, resolve) {
		t.Errorf("embedded workflow set inconsistent: %s", p)
	}
	for _, sk := range defaultSolutionSkills {
		body, ok := embeddedDefault("skills", sk)
		if !ok {
			t.Fatalf("embedded skill %s missing", sk)
		}
		for _, p := range structure.Doc("skills", sk, body, nil) {
			t.Errorf("embedded skills/%s: %s", sk, p)
		}
	}

	// An execution resolves to the route's task section out of the box, gated by
	// the two task-validate reviewers — never falling through to the wildcard.
	exec := embeddedRouteFor(t, "execution")
	if !hasGate(exec, "backlog", "in_progress", "satelle-task-validate-before-review") ||
		!hasGate(exec, "in_progress", "done", "satelle-task-validate-after-review") {
		t.Errorf("execution does not resolve to the route's task section: %+v", exec.Transitions)
	}
	for _, st := range exec.States {
		if st.Obligation == "coded" {
			t.Error("an execution fell through to the wildcard lane (it owes `coded`)")
		}
	}

	// The generic lane stays generic: no this-repo spine, and every gate the
	// retired baseline carried is still declared.
	wild := embeddedRouteFor(t, "*")
	declared := map[string]bool{}
	for _, st := range wild.States {
		declared[st.Name] = true
		if st.Skill != "" {
			declared[st.Skill] = true
		}
	}
	for _, tr := range wild.Transitions {
		for _, sk := range tr.Skills {
			declared[sk] = true
		}
	}
	for _, state := range []string{"commit", "push", "committed", "integration", "plan", "release"} {
		if declared[state] {
			t.Errorf("the generic lane declares extra state %q", state)
		}
	}
	for _, gate := range []string{"satelle-story-intent-review", "satelle-story-done-review", "satelle-story-cancel-review", "satelle-estimate-actual-review", "satelle-story-scope-review", "satelle-workflow-change-review", "satelle-story-blocked-review", "satelle-step-summary"} {
		if !declared[gate] {
			t.Errorf("the generic lane must declare gate %q", gate)
		}
	}
	if declared["satelle-code-ac-review"] {
		t.Error("the generic lane must not reference satelle-code-ac-review")
	}
	estBody, _ := embeddedDefault("skills", "satelle-estimate-actual-review")
	if !strings.Contains(estBody, "```check") {
		t.Error("estimate skill must carry a self-contained ```check block")
	}
}

// TestRunInitBesideAuthoredWorkflowNoSeeds (sty_29e5a9a5): with virtual defaults,
// init does not seed sibling workflow/skill files beside an authored workflow.
// Gate skills resolve virtually; validation is green; the authored file is untouched.
func TestRunInitBesideAuthoredWorkflowNoSeeds(t *testing.T) {
	repo := t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(wfDir, "done.toml")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[[gate]]
skill = "satelle-estimate-actual-review"
on = ["in_progress", "done"]
`)
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	// No default seeds.
	if fileExists(filepath.Join(repo, ".satelle", "skills/satelle-estimate-actual-review.md")) {
		t.Error("init seeded skills/satelle-estimate-actual-review.md — virtual defaults must not write unedited copies")
	}
	if got, _ := os.ReadFile(own); !strings.Contains(string(got), "fixture declaration of done") {
		t.Error("the authored route source was modified")
	}
}

// TestRunInitVirtualGateSkillResolves (sty_29e5a9a5): a workflow that references
// an embedded gate skill validates green without the skill file on disk.
func TestRunInitVirtualGateSkillResolves(t *testing.T) {
	repo := t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[[gate]]
skill = "satelle-estimate-actual-review"
on = ["in_progress", "done"]
`)
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("init must validate green with virtual gate skill: %v", err)
	}
	if fileExists(filepath.Join(repo, ".satelle", "skills", "satelle-estimate-actual-review.md")) {
		t.Error("init must not seed the virtual gate skill onto disk")
	}
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
	old := "node_modules/\n# >>> satelle (managed) >>>\n# an older wording of the header\n.satelle/\n.satelle/satelle.db\n.satelle/logs/\nsty_*_body.md\nsty_*_ac.md\n# <<< satelle (managed) <<<\n# keep-me\n"
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

// TestRunInitIgnoresFreshHarnessDirOnce (sty_dbc0be85 AC4): init scaffolds the
// harness hooks before it writes .gitignore, so a fresh `--harness claude` adds
// .claude/ on the first run and a second run leaves the bytes equal.
func TestRunInitIgnoresFreshHarnessDirOnce(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo)
	isolateUserHome(t)
	giPath := filepath.Join(repo, ".gitignore")
	if err := runInit(io.Discard, repo, true, []string{"claude"}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	first, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatal(err)
	}
	if !gitignoreHasRule(string(first), ".claude") {
		t.Fatalf("first run must add .claude/:\n%s", first)
	}
	if err := runInit(io.Discard, repo, true, []string{"claude"}); err != nil {
		t.Fatalf("second init: %v", err)
	}
	second, _ := os.ReadFile(giPath)
	if string(first) != string(second) {
		t.Errorf("second run changed .gitignore:\n--- first\n%s\n--- second\n%s", first, second)
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

// TestRunInitAgentGuidance asserts init ends its report with the agent-facing
// note when the repo carries an agent instruction file (sty_4c406061): the
// reading agent is told to add/update a satelle section preferring `satelle
// help` — and the file itself is never modified.
func TestRunInitAgentGuidance(t *testing.T) {
	cases := []struct {
		name  string
		files []string
	}{
		{"claude.md present", []string{"CLAUDE.md"}},
		{"agents.md present", []string{"AGENTS.md"}},
		{"both present", []string{"CLAUDE.md", "AGENTS.md"}},
		{"case-insensitive", []string{"claude.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			owned := "# My instructions\n\nkeep me\n"
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(repo, f), []byte(owned), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out strings.Builder
			if err := runInitTest(t, &out, repo); err != nil {
				t.Fatalf("runInit: %v", err)
			}
			for _, f := range c.files {
				if !strings.Contains(out.String(), f) {
					t.Errorf("agent note does not name %s:\n%s", f, out.String())
				}
			}
			for _, want := range []string{"Agent note:", `"## satelle" section`, "satelle help", "prefer that over duplicating"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("agent note missing %q:\n%s", want, out.String())
				}
			}
			// AC1 (sty_fd4b1cd4): the note points custom/process agents at
			// .satelle/agents.toml, explicitly NOT a harness-specific agent dir.
			for _, want := range []string{"agents.toml", ".claude/agents"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("agent note missing the agents-layer pointer %q:\n%s", want, out.String())
				}
			}
			// AC4: the seeded agents.toml scaffold names the anti-pattern.
			toml, _ := os.ReadFile(filepath.Join(repo, ".satelle", "workflows", "agents.toml"))
			for _, want := range []string{".claude/agents", "invisible", "one CLI vendor"} {
				if !strings.Contains(string(toml), want) {
					t.Errorf("seeded agents.toml missing anti-pattern comment %q:\n%s", want, toml)
				}
			}
			// init never edits the agent-owned file.
			for _, f := range c.files {
				if got, _ := os.ReadFile(filepath.Join(repo, f)); string(got) != owned {
					t.Errorf("init modified %s:\n%s", f, got)
				}
			}
		})
	}

	t.Run("neither present", func(t *testing.T) {
		repo := t.TempDir()
		var out strings.Builder
		if err := runInitTest(t, &out, repo); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "Agent note:") {
			t.Errorf("agent note emitted with no instruction file present:\n%s", out.String())
		}
	})
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

// TestRunInitMigratesDriftedAgentsToml: re-init format-migrates harness= → command=
// and adds missing role= (order:7), printing a ~ migrated line; post-migrate
// agentvalidate has zero WARN.
func TestRunInitMigratesDriftedAgentsToml(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("seed init: %v", err)
	}
	path := filepath.Join(repo, config.DefaultDataDir, config.AgentsConfigDir, config.AgentsConfigName)
	drifted := `[executor]
harness = "in-loop"

[reviewer]
harness = "claude"
tools   = "Read,Grep,Glob"
`
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("re-init: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "migrated:") || !strings.Contains(out.String(), "agents.toml") {
		t.Fatalf("want ~ migrated line for agents.toml, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)
	if strings.Contains(body, "harness") {
		t.Fatalf("harness not rewritten:\n%s", body)
	}
	if !strings.Contains(body, `command = "in-loop"`) || !strings.Contains(body, `role = "agent"`) {
		t.Fatalf("migration incomplete:\n%s", body)
	}
	if !strings.Contains(body, `role = "reviewer"`) {
		t.Fatalf("reviewer role missing:\n%s", body)
	}
	agents, err := config.LoadAgents(filepath.Join(repo, config.DefaultDataDir))
	if err != nil {
		t.Fatal(err)
	}
	report := agentvalidate.Validate(agents, deployedVars(filepath.Join(repo, config.DefaultDataDir)), deployedWorkflowDocs(filepath.Join(repo, config.DefaultDataDir)))
	for _, w := range report.Warnings {
		if strings.Contains(w, "role") || strings.Contains(w, "inferred") {
			t.Fatalf("role-inferred WARN after migrate: %v", report.Warnings)
		}
	}
	if !report.OK() {
		t.Fatalf("problems after migrate: %v", report.Problems)
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

// AC1 (sty_b336c2b0): init removes every removed [sync] scope key from
// satelle.toml and satelle.local.toml on raw bytes, before the heals that load
// the config, and keeps the binding and the other [sync] keys.
func TestInitRemovesSyncAreaScopeKeys(t *testing.T) {
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := "[sync]\nall = \"personal\"\nskills = \"local\"\nproject = \"probe\"\nstale_after = \"6h\"\n\n[hosted]\nproject = \"probe\"\n"
	local := "[sync]\ndocuments = \"local\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, config.ConfigName), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.LocalConfigName), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err == nil {
		t.Fatal("test premise: a config carrying scope keys must not load")
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	found, err := config.FindSyncScopeKeys(dataDir)
	if err != nil || len(found) != 0 {
		t.Fatalf("init left scope keys: %v (%v)", found, err)
	}
	got, _ := os.ReadFile(filepath.Join(dataDir, config.ConfigName))
	for _, keep := range []string{`project = "probe"`, `stale_after = "6h"`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("init dropped %q:\n%s", keep, got)
		}
	}
	if strings.Contains(string(got), "[hosted]") {
		t.Errorf("the [hosted] fold did not run after the scope keys were removed:\n%s", got)
	}
	cfg, _, err := config.Load(filepath.Join(dataDir, config.ConfigName))
	if err != nil {
		t.Fatalf("the healed config does not load: %v", err)
	}
	if cfg.SyncProject() != "probe" {
		t.Errorf("[sync] project = %q, want probe", cfg.SyncProject())
	}
	if !strings.Contains(out.String(), "removed retired [sync] keys") {
		t.Errorf("init output does not report the removal:\n%s", out.String())
	}
}

// AC9 (sty_b336c2b0): init --all reaches a registered repo whose hooks are
// current but whose config still carries a scope key, heals it, and then counts
// it current.
func TestInitAllHealsSyncScopeKeysWithCurrentHooks(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	if err := runInit(io.Discard, repo, false, []string{"claude"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("test premise: a freshly initialised repo is current: %v", fs)
	}
	cfgPath := filepath.Join(repo, ".satelle", config.ConfigName)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(b), "[sync]\n", "[sync]\nall = \"personal\"\n", 1)
	if legacy == string(b) {
		t.Fatalf("test premise: the scaffold has no [sync] table:\n%s", b)
	}
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runInitAll(&out, true, nil); err != nil {
		t.Fatalf("init --all --yes: %v\n%s", err, out.String())
	}
	for _, want := range []string{"STALE   " + repo, "[sync] all is removed", "HEALED  " + repo} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("init --all output lacks %q:\n%s", want, out.String())
		}
	}
	if found, err := config.FindSyncScopeKeys(filepath.Dir(cfgPath)); err != nil || len(found) != 0 {
		t.Errorf("init --all left scope keys: %v (%v)", found, err)
	}

	out.Reset()
	if err := runInitAll(&out, true, nil); err != nil {
		t.Fatalf("second init --all: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "STALE") || !strings.Contains(out.String(), "1 already current") {
		t.Errorf("a healed repo must count as current:\n%s", out.String())
	}
}

// sty_7c01ca14 AC5: init removes [sync] recent_window from satelle.toml and
// satelle.local.toml, keeps every other [sync] key, and leaves a config that loads.
func TestInitRemovesRecentWindow(t *testing.T) {
	testutil.IsolateHome(t) // scaffold drift reads the machine catalog (sty_ba9f5d14)
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := "[sync]\nproject = \"probe\"\nrecent_window = \"168h\"\nstale_after = \"6h\"\n"
	local := "[sync]\nrecent_window = \"24h\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, config.ConfigName), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.LocalConfigName), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err == nil || !strings.Contains(err.Error(), "[sync] recent_window") {
		t.Fatalf("test premise: a config carrying recent_window must not load, got %v", err)
	}
	if fs := DetectScaffoldDrift(repo); !slices.ContainsFunc(fs, func(f ScaffoldFinding) bool {
		return strings.Contains(f.Detail, "[sync] recent_window is removed")
	}) {
		t.Errorf("drift does not report recent_window: %v", fs)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	if found, err := config.FindSyncScopeKeys(dataDir); err != nil || len(found) != 0 {
		t.Fatalf("init left removed keys: %v (%v)", found, err)
	}
	got, _ := os.ReadFile(filepath.Join(dataDir, config.ConfigName))
	for _, keep := range []string{`project = "probe"`, `stale_after = "6h"`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("init dropped %q:\n%s", keep, got)
		}
	}
	if _, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err != nil {
		t.Fatalf("the healed config does not load: %v", err)
	}
}
