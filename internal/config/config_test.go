package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZeroConfigDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	var c Config
	repo := "/repo"
	if got := c.ResolveDataDir(repo); got != "/repo/.satelle" {
		t.Errorf("ResolveDataDir = %q", got)
	}
	// Default DB is home-keyed under ~/.satelle/<repo-key>/ (sty_4660bbe1).
	wantDB := filepath.Join(home, RepoKey(repo), DefaultDBName)
	if got := c.ResolveDB(repo); got != wantDB {
		t.Errorf("ResolveDB = %q, want %q", got, wantDB)
	}
	if got := c.ResolveLogLevel(); got != "info" {
		t.Errorf("ResolveLogLevel = %q", got)
	}
}

func TestResolveDBOverride(t *testing.T) {
	c := Config{DB: "/abs/custom.db"}
	if got := c.ResolveDB("/repo"); got != "/abs/custom.db" {
		t.Errorf("absolute db override = %q", got)
	}
	c = Config{DB: "data/x.db"}
	if got := c.ResolveDB("/repo"); got != "/repo/data/x.db" {
		t.Errorf("relative db override = %q", got)
	}
}

func TestResolveEditExemptPaths(t *testing.T) {
	// Unset → nil (default binary exempts only the data dir).
	var zero Config
	if got := zero.ResolveEditExemptPaths("/repo"); got != nil {
		t.Errorf("unconfigured ResolveEditExemptPaths = %v, want nil", got)
	}
	// Blanks/whitespace dropped; relative resolved under repo; absolute passes
	// through. filepath.Join strips the trailing slash of ".claude/".
	c := Config{Gate: GateConfig{EditExemptPaths: []string{".claude/", "", "  ", "/opt/authoring"}}}
	got := c.ResolveEditExemptPaths("/repo")
	want := []string{"/repo/.claude", "/opt/authoring"}
	if len(got) != len(want) {
		t.Fatalf("ResolveEditExemptPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ResolveEditExemptPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveEditExemptGlobs(t *testing.T) {
	var zero Config
	if got := zero.ResolveEditExemptGlobs(); got != nil {
		t.Errorf("unconfigured ResolveEditExemptGlobs = %v, want nil", got)
	}
	c := Config{Gate: GateConfig{EditExemptGlobs: []string{"sty_*_body.md", "", "  ", "sty_*_ac.md"}}}
	got := c.ResolveEditExemptGlobs()
	want := []string{"sty_*_body.md", "sty_*_ac.md"}
	if len(got) != len(want) {
		t.Fatalf("ResolveEditExemptGlobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ResolveEditExemptGlobs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	allBlank := Config{Gate: GateConfig{EditExemptGlobs: []string{"", "  "}}}
	if got := allBlank.ResolveEditExemptGlobs(); got != nil {
		t.Errorf("all-blank ResolveEditExemptGlobs = %v, want nil", got)
	}
}

func TestRetrieveKeepDaysDefaultZeroAndReadFromToml(t *testing.T) {
	var zero Config
	if zero.RetrieveKeepDays != 0 {
		t.Errorf("zero-value RetrieveKeepDays = %d, want 0 (keep forever)", zero.RetrieveKeepDays)
	}

	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := "retrieve_keep_days = 45\n"
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(filepath.Join(satelleDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetrieveKeepDays != 45 {
		t.Errorf("RetrieveKeepDays = %d, want 45", cfg.RetrieveKeepDays)
	}
}

func TestLoadWithLocalOverlay(t *testing.T) {
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := "log_level = \"warn\"\nstories_keep_closed = 10\n"
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	// Overlay overrides stories_keep_closed but not log_level.
	local := "stories_keep_closed = 3\n"
	if err := os.WriteFile(filepath.Join(satelleDir, LocalConfigName), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := Load(filepath.Join(satelleDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StoriesKeepClosed != 3 {
		t.Errorf("overlay stories_keep_closed = %d, want 3", cfg.StoriesKeepClosed)
	}
	if cfg.ResolveLogLevel() != "warn" {
		t.Errorf("committed log_level lost: %q", cfg.ResolveLogLevel())
	}
	if RepoRootFromConfigPath(path) != repo {
		t.Errorf("repo root = %q, want %q", RepoRootFromConfigPath(path), repo)
	}
}

// writeConfig lands a satelle.toml (and optional overlay) and Loads it.
func writeConfig(t *testing.T, committed string) (Config, error) {
	t.Helper()
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(filepath.Join(satelleDir, ConfigName))
	return cfg, err
}

// TestEngagementParallelDefault (sty_c098dc2d AC1): an absent [engagement]
// section, and an explicit "none", both resolve to single occupancy. The zero
// value matters most — every repo that never heard of the mode keeps today's
// behaviour with no config change.
func TestEngagementParallelDefault(t *testing.T) {
	var zero Config
	if got := zero.ResolveEngagementParallel(); got != ParallelNone {
		t.Errorf("zero Config mode = %q, want %q", got, ParallelNone)
	}
	cfg, err := writeConfig(t, "log_level = \"info\"\n")
	if err != nil {
		t.Fatalf("absent [engagement] must load: %v", err)
	}
	if got := cfg.ResolveEngagementParallel(); got != ParallelNone {
		t.Errorf("absent section mode = %q, want %q", got, ParallelNone)
	}
	cfg, err = writeConfig(t, "[engagement]\nparallel = \"none\"\n")
	if err != nil {
		t.Fatalf("explicit none must load: %v", err)
	}
	if got := cfg.ResolveEngagementParallel(); got != ParallelNone {
		t.Errorf("explicit none = %q", got)
	}
	// A MISSPELLED section is ignored by bare toml decoding and so fails closed
	// to the default — a documented property, pinned so it cannot silently
	// become "unknown mode" or, worse, an accidental opt-in.
	cfg, err = writeConfig(t, "[engagment]\nparallel = \"epic\"\n")
	if err != nil {
		t.Fatalf("typo'd section must not error: %v", err)
	}
	if got := cfg.ResolveEngagementParallel(); got != ParallelNone {
		t.Errorf("typo'd section mode = %q, want %q (fail closed)", got, ParallelNone)
	}
}

// TestEngagementParallelValidation (sty_c098dc2d AC6): the mode set is CLOSED.
// "epic" loads; anything else — including "all", a recorded non-goal, and a
// case variant — is refused at load naming the supported modes.
func TestEngagementParallelValidation(t *testing.T) {
	cfg, err := writeConfig(t, "[engagement]\nparallel = \"epic\"\n")
	if err != nil {
		t.Fatalf("epic must load: %v", err)
	}
	if got := cfg.ResolveEngagementParallel(); got != ParallelEpic {
		t.Errorf("epic mode = %q", got)
	}
	cfg, err = writeConfig(t, "[engagement]\nparallel = \"worktree\"\n")
	if err != nil {
		t.Fatalf("worktree must load: %v", err)
	}
	if got := cfg.ResolveEngagementParallel(); got != ParallelWorktree {
		t.Errorf("worktree mode = %q", got)
	}
	// Matching is exact — case variants are refused so the configured surface
	// stays the one the docs name.
	for _, bad := range []string{"all", "EPIC", "Epic", "sibling", "true"} {
		_, err := writeConfig(t, "[engagement]\nparallel = \""+bad+"\"\n")
		if err == nil {
			t.Errorf("parallel = %q must be refused at load", bad)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, ParallelNone) || !strings.Contains(msg, ParallelEpic) || !strings.Contains(msg, ParallelWorktree) {
			t.Errorf("refusal for %q must name all three modes: %v", bad, err)
		}
		if !strings.Contains(msg, bad) {
			t.Errorf("refusal for %q must quote the offending value: %v", bad, err)
		}
	}
}

// TestGateHandoff (sty_8ee31f26 AC1, AC3): [gate] handoff is a closed set that
// defaults to auto, round-trips through Load and the settings display, and a
// value outside the set is refused at load naming the supported values.
func TestGateHandoff(t *testing.T) {
	var zero Config
	if got := zero.ResolveGateHandoff(); got != GateHandoffAuto {
		t.Errorf("zero Config handoff = %q, want %q", got, GateHandoffAuto)
	}
	for _, want := range []string{GateHandoffAuto, GateHandoffOn, GateHandoffOff} {
		cfg, err := writeConfig(t, "[gate]\nhandoff = \""+want+"\"\n")
		if err != nil {
			t.Fatalf("handoff = %q must load: %v", want, err)
		}
		if got := cfg.ResolveGateHandoff(); got != want {
			t.Errorf("handoff = %q resolved to %q", want, got)
		}
		s, ok := SettingByID("gate.handoff")
		if !ok {
			t.Fatal("gate.handoff is not in the settings registry")
		}
		if got := SettingDisplay(cfg, s); got != want {
			t.Errorf("settings display = %q, want %q", got, want)
		}
		if _, err := s.EncodeValue(want); err != nil {
			t.Errorf("EncodeValue(%q): %v", want, err)
		}
	}
	for _, bad := range []string{"ON", "true", "yes", "always"} {
		_, err := writeConfig(t, "[gate]\nhandoff = \""+bad+"\"\n")
		if err == nil {
			t.Errorf("handoff = %q must be refused at load", bad)
			continue
		}
		for _, name := range []string{GateHandoffAuto, GateHandoffOn, GateHandoffOff, bad} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("refusal for %q must name %q: %v", bad, name, err)
			}
		}
	}
}

// TestLoadVarsOverlay pins the per-key merge the env substitution relies on: a
// committed [vars] provides non-secret defaults, and the gitignored local overlay
// adds/overrides keys WITHOUT dropping committed-only keys (sty_001558ce).
func TestLoadVarsOverlay(t *testing.T) {
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := "[vars]\nA = \"1\"\nB = \"2\"\n"
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	// Overlay overrides B and adds C; A (committed only) must survive.
	local := "[vars]\nB = \"9\"\nC = \"3\"\n"
	if err := os.WriteFile(filepath.Join(satelleDir, LocalConfigName), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(filepath.Join(satelleDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "9", "C": "3"}
	for k, v := range want {
		if cfg.Vars[k] != v {
			t.Errorf("vars[%q] = %q, want %q (full: %v)", k, cfg.Vars[k], v, cfg.Vars)
		}
	}
}

// TestLoadSyncOverlay pins the same per-key merge for [sync]: a committed table
// sets the binding for the team, and the gitignored local overlay overrides a
// single key WITHOUT dropping the committed-only keys (sty_a2d2e057, plan
// addendum #1).
func TestLoadSyncOverlay(t *testing.T) {
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := "[sync]\nproject = \"team-proj\"\nworkspace = \"Acme\"\n"
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	// Overlay overrides workspace; project (committed only) must survive.
	local := "[sync]\nworkspace = \"Solo\"\n"
	if err := os.WriteFile(filepath.Join(satelleDir, LocalConfigName), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(filepath.Join(satelleDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"project": "team-proj", "workspace": "Solo"}
	for k, v := range want {
		if cfg.Sync[k] != v {
			t.Errorf("sync[%q] = %q, want %q (full: %v)", k, cfg.Sync[k], v, cfg.Sync)
		}
	}
}

// TestLoadDispatchLeftovers covers the [dispatch.leftovers] table
// (sty_e7aaf8b1 AC6): the leftover-sweep rule loads from committed config,
// and an absent table leaves the zero value (sweep disabled).
func TestLoadDispatchLeftovers(t *testing.T) {
	repo := t.TempDir()
	satelleDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := "[dispatch.leftovers]\n" +
		"patterns = [\".ac-evidence*\"]\n" +
		"content_regex = '^\\s*package \\w+\\s*$'\n" +
		"max_bytes = 64\n" +
		"action = \"flag\"\n"
	if err := os.WriteFile(filepath.Join(satelleDir, ConfigName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(filepath.Join(satelleDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	rule := cfg.Dispatch.Leftovers
	if len(rule.Patterns) != 1 || rule.Patterns[0] != ".ac-evidence*" {
		t.Errorf("patterns = %v", rule.Patterns)
	}
	if rule.MaxBytes != 64 {
		t.Errorf("max_bytes = %d", rule.MaxBytes)
	}
	if rule.ResolveAction() != LeftoverActionFlag {
		t.Errorf("ResolveAction = %q, want flag", rule.ResolveAction())
	}

	var empty Config
	if empty.Dispatch.Leftovers.ResolveAction() != LeftoverActionMove {
		t.Errorf("zero-value rule ResolveAction = %q, want move (the default)", empty.Dispatch.Leftovers.ResolveAction())
	}
	if len(empty.Dispatch.Leftovers.Patterns) != 0 || empty.Dispatch.Leftovers.ContentRegex != "" {
		t.Error("zero-value rule must ship with no patterns and no regex — the binary has no opinion")
	}
}

// useFixtureEnv replaces the whole process environment with an agentcli
// harness fixture (restored on cleanup), so the test never depends on the
// harness it happens to run under.
func useFixtureEnv(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agentcli", "testdata", "harness", name+".env"))
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Environ()
	os.Clearenv()
	t.Cleanup(func() {
		os.Clearenv()
		for _, e := range saved {
			k, v, _ := strings.Cut(e, "=")
			_ = os.Setenv(k, v)
		}
	})
	for _, e := range strings.Fields(string(b)) {
		k, v, _ := strings.Cut(e, "=")
		_ = os.Setenv(k, v)
	}
}

func TestIsAgentCaller(t *testing.T) {
	for _, c := range []struct {
		fixture string
		want    bool
	}{
		{"plain", false},
		{"claude", true},
		{"grok", true},
	} {
		useFixtureEnv(t, c.fixture)
		if got := IsAgentCaller(); got != c.want {
			t.Errorf("IsAgentCaller() under %s fixture = %v, want %v", c.fixture, got, c.want)
		}
	}
	useFixtureEnv(t, "plain")
	t.Setenv(ScratchEnv, "/tmp/satelle/scratch")
	if !IsAgentCaller() {
		t.Error("IsAgentCaller() = false with SATELLE_SCRATCH set, want true")
	}
	t.Setenv(ScratchEnv, "")
	t.Setenv(SessionEnv, "sess-1")
	if !IsAgentCaller() {
		t.Error("IsAgentCaller() = false with SATELLE_SESSION set, want true")
	}
}
