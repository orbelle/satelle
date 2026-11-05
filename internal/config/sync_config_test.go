package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes a file under repo, creating parent dirs.
func writeFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	dest := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findConfigFile(files []ConfigFile, path string) (ConfigFile, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return ConfigFile{}, false
}

// TestDocumentFilesScopeLocalSkipped: under a local scope the documents area
// contributes nothing.
func TestDocumentFilesScopeLocalSkipped(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/documents/note.md", "body")
	b, err := DocumentFiles(Config{}, LocalScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("DocumentFiles: %v", err)
	}
	if len(b.Files) != 0 {
		t.Errorf("local documents walk returned %d files, want 0", len(b.Files))
	}
}

// TestConfigAreasExcludesDocumentsAndWorkState: the candidate set is the authored
// config areas + tasks + settings (satelle.toml), never documents (its own kind)
// or the work-state areas.
func TestConfigAreasExcludesDocumentsAndWorkState(t *testing.T) {
	want := map[string]bool{"workflows": true, "principles": true, "skills": true, "constitution": true, "agents": true, "tasks": true, "settings": true}
	seen := map[string]bool{}
	for _, a := range ConfigAreas {
		seen[a] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("ConfigAreas missing %q", k)
		}
	}
	for _, bad := range []string{"documents", "stories", "ledger", "executions"} {
		if seen[bad] {
			t.Errorf("ConfigAreas must not include %q", bad)
		}
	}
}

// SubstrateAreas is the config areas plus documents under a non-local scope,
// and none under a local one.
func TestSubstrateAreasFollowScope(t *testing.T) {
	if got := SubstrateAreas(LocalScope); len(got) != 0 {
		t.Fatalf("local scope = %v, want none", got)
	}
	for _, scope := range []Scope{PersonalScope, TeamScope} {
		got := SubstrateAreas(scope)
		if len(got) != len(ConfigAreas)+1 || got[len(got)-1] != "documents" {
			t.Fatalf("%s scope = %v, want every config area plus documents", scope, got)
		}
	}
}

// A settings file holding only what a push withholds is a placeholder; anything
// authored is not.
func TestPlaceholderSettings(t *testing.T) {
	for body, want := range map[string]bool{
		"":                                 true,
		"[sync]\nproject = \"p\"\n":        true,
		"[hosted]\nproject = \"p\"\n":      true,
		"# a comment\n[sync]\n":            true,
		"[sync]\nstale_after = \"1h\"\n":   false,
		"[sync]\nproject = \"p\"\nx = 1\n": false,
		"[review]\ngate_create = false\n":  false,
		"this is not toml [":               false,
	} {
		repo := t.TempDir()
		writeFile(t, repo, ".satelle/satelle.toml", body)
		got, err := PlaceholderSettings(Config{}, repo, Ignore{})
		if err != nil || got != want {
			t.Errorf("PlaceholderSettings(%q) = %v, %v; want %v", body, got, err, want)
		}
	}
	// No satelle.toml at all is a placeholder too.
	if got, err := PlaceholderSettings(Config{}, t.TempDir(), Ignore{}); err != nil || !got {
		t.Errorf("a missing satelle.toml = %v, %v", got, err)
	}
}

// TestConfigFilesSkipsLocalArea: a local scope contributes nothing.
func TestConfigFilesSkipsLocalArea(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/a.md", "body")
	b, err := ConfigFiles(Config{}, LocalScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if len(b.Files) != 0 {
		t.Errorf("local-scope walk returned %d files, want 0: %+v", len(b.Files), b.Files)
	}
}

// TestConfigFilesTierResolution: a team scope is SharedTier wholesale; a
// personal scope is PersonalTier per file, PROMOTED to SharedTier when the file
// is frontmarked shared (AC1 honors the per-file shared flag).
func TestConfigFilesTierResolution(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/personal-one.md", "---\ntype: skill\n---\nbody\n")
	writeFile(t, repo, ".satelle/skills/shared-one.md", "---\ntype: skill\nshared: true\n---\nbody\n")
	writeFile(t, repo, ".satelle/principles/team-rule.md", "---\ntype: principle\n---\nbody\n")
	writeFile(t, repo, ".satelle/workflows/agents.toml", "[executor]\nharness = \"in-loop\"\n")
	// The synced workspace layer (sty_01949949) lives beside agents.toml and is
	// never pushed back as config — not under workflows/, not under agents.
	writeFile(t, repo, ".satelle/workflows/agents.workspace.toml", "[reviewer]\nrole = \"reviewer\"\n")

	b, err := ConfigFiles(Config{}, PersonalScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if _, ok := findConfigFile(b.Files, "workflows/agents.workspace.toml"); ok {
		t.Error("agents.workspace.toml must never be pushed as config")
	}
	// skills/personal-one.md -> PersonalTier (personal area, no shared flag).
	if f, ok := findConfigFile(b.Files, "skills/personal-one.md"); !ok {
		t.Error("missing skills/personal-one.md")
	} else if f.Tier != PersonalTier {
		t.Errorf("personal-one tier = %v, want PersonalTier", f.Tier)
	}
	// skills/shared-one.md -> SharedTier (personal area, shared flag promotes it).
	if f, ok := findConfigFile(b.Files, "skills/shared-one.md"); !ok {
		t.Error("missing skills/shared-one.md")
	} else if f.Tier != SharedTier {
		t.Errorf("shared-one tier = %v, want SharedTier", f.Tier)
	}
	// principles/team-rule.md -> PersonalTier (personal scope, no shared flag).
	if f, ok := findConfigFile(b.Files, "principles/team-rule.md"); !ok {
		t.Error("missing principles/team-rule.md")
	} else if f.Tier != PersonalTier {
		t.Errorf("team-rule tier = %v, want PersonalTier", f.Tier)
	}
	// agents.toml -> PersonalTier (personal area, TOML has no frontmatter).
	if f, ok := findConfigFile(b.Files, "workflows/agents.toml"); !ok {
		t.Error("missing agents.toml")
	} else if f.Tier != PersonalTier {
		t.Errorf("agents.toml tier = %v, want PersonalTier", f.Tier)
	}

	// A team scope labels every file SharedTier wholesale.
	team, err := ConfigFiles(Config{}, TeamScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if len(team.Files) == 0 {
		t.Fatal("team scope walked nothing")
	}
	for _, f := range team.Files {
		if f.Tier != SharedTier {
			t.Errorf("%s tier under team scope = %v, want SharedTier", f.Path, f.Tier)
		}
	}
}

// TestConfigFilesExcludesReservedViews: the generated index.md/log.md and a
// README are never part of a push (they are not authored substrate).
func TestConfigFilesExcludesReservedViews(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/keep.md", "body")
	writeFile(t, repo, ".satelle/skills/index.md", "generated")
	writeFile(t, repo, ".satelle/skills/log.md", "generated")
	writeFile(t, repo, ".satelle/skills/README.md", "readme")
	b, err := ConfigFiles(Config{}, TeamScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if _, ok := findConfigFile(b.Files, "skills/keep.md"); !ok {
		t.Error("keep.md should be included")
	}
	for _, drop := range []string{"skills/index.md", "skills/log.md", "skills/README.md"} {
		if f, ok := findConfigFile(b.Files, drop); ok {
			t.Errorf("reserved view %q should be excluded: %+v", drop, f)
		}
	}
}

// TestConfigFilesServerPathStableUnderSubstrateRoots: when [substrate_roots]
// relocates a kind outside .satelle/, the server path is still the stable
// area/relpath key (not the absolute on-disk path) — so a deploy restores to a
// fresh repo identically.
func TestConfigFilesServerPathStableUnderSubstrateRoots(t *testing.T) {
	repo := t.TempDir()
	alt := filepath.Join(repo, "elsewhere")
	writeFile(t, repo, "elsewhere/skills/relocated.md", "body")
	cfg := Config{SubstrateRoots: map[string]string{"skills": alt}}
	b, err := ConfigFiles(cfg, TeamScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if f, ok := findConfigFile(b.Files, "skills/relocated.md"); !ok {
		t.Errorf("relocated skill missing; files=%+v", b.Files)
	} else if string(f.Content) != "body" {
		t.Errorf("content = %q", f.Content)
	}
}

// TestConfigFilesSortedDeterministically: the walk output is sorted by Path.
func TestConfigFilesSortedDeterministically(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/zeta.md", "z")
	writeFile(t, repo, ".satelle/skills/alpha.md", "a")
	writeFile(t, repo, ".satelle/principles/mid.md", "m")
	b, err := ConfigFiles(Config{}, TeamScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	for i := 1; i < len(b.Files); i++ {
		if b.Files[i-1].Path > b.Files[i].Path {
			t.Errorf("not sorted: %q before %q", b.Files[i-1].Path, b.Files[i].Path)
		}
	}
}

// TestLocalOnlyPath: the .local exclusion matcher — positives and D2 negatives.
func TestLocalOnlyPath(t *testing.T) {
	yes := []string{
		"satelle.local.toml",
		"skills/my-thing.local.md",
		"documents/notes.LOCAL.md",
		"x.local",
		".local",
		"secrets.local/keys.md",
		"x.local.yaml",
	}
	for _, p := range yes {
		if !LocalOnlyPath(p) {
			t.Errorf("LocalOnlyPath(%q) = false, want true", p)
		}
	}
	no := []string{
		"skills/local/x.md",
		"local.md",
		"mylocal.md",
		"locale.md",
		"skills/keep.md",
		"agents.toml",
		"constitution.md",
	}
	for _, p := range no {
		if LocalOnlyPath(p) {
			t.Errorf("LocalOnlyPath(%q) = true, want false", p)
		}
	}
}

// TestAssembleWithholdsLocalFiles: assemble is the sole exclusion seam.
func TestAssembleWithholdsLocalFiles(t *testing.T) {
	in := []ConfigFile{
		{Path: "skills/keep.md", Content: []byte("ok")},
		{Path: "skills/secret.local.md", Content: []byte("secret")},
		{Path: "notes.local.md", Content: []byte("x")},
	}
	b := assemble(in, Ignore{})
	if len(b.Files) != 1 || b.Files[0].Path != "skills/keep.md" {
		t.Fatalf("Files = %+v, want only skills/keep.md", b.Files)
	}
	if len(b.Withheld) != 2 {
		t.Fatalf("Withheld = %v, want 2", b.Withheld)
	}
	// Withheld is sorted.
	if b.Withheld[0] != "notes.local.md" || b.Withheld[1] != "skills/secret.local.md" {
		t.Errorf("Withheld not sorted: %v", b.Withheld)
	}
}

// TestSkillsAreaWithholdsLocalFile: the case that is NOT protected today —
// a .local file inside an opted-in authored area.
func TestSkillsAreaWithholdsLocalFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/keep.md", "body")
	writeFile(t, repo, ".satelle/skills/my-thing.local.md", "SECRET")
	b, err := ConfigFiles(Config{}, PersonalScope, repo, Ignore{})
	if err != nil {
		t.Fatalf("ConfigFiles: %v", err)
	}
	if _, ok := findConfigFile(b.Files, "skills/keep.md"); !ok {
		t.Error("keep.md missing from Files")
	}
	if _, ok := findConfigFile(b.Files, "skills/my-thing.local.md"); ok {
		t.Error("my-thing.local.md must not be in Files")
	}
	found := false
	for _, w := range b.Withheld {
		if w == "skills/my-thing.local.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("Withheld missing skills/my-thing.local.md: %v", b.Withheld)
	}
}

// TestLocalExclusionIgnoresConfig: LocalOnlyPath takes no Config — withholding
// holds under every resolved scope and under shared:true frontmatter (AC4).
func TestLocalExclusionIgnoresConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/skills/keep.md", "ok")
	writeFile(t, repo, ".satelle/skills/secret.local.md", "---\nshared: true\n---\nSECRET\n")

	cases := []Scope{LocalScope, PersonalScope, TeamScope}
	for i, scope := range cases {
		b, err := ConfigFiles(Config{}, scope, repo, Ignore{})
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		// Local scope → nothing walked; still no secret in Files.
		if _, ok := findConfigFile(b.Files, "skills/secret.local.md"); ok {
			t.Errorf("case %d: secret.local.md in Files under scope %s", i, scope)
		}
		if scope == LocalScope {
			continue // local scope — Withheld is empty because walk never saw it
		}
		found := false
		for _, w := range b.Withheld {
			if w == "skills/secret.local.md" {
				found = true
			}
		}
		if !found {
			t.Errorf("case %d: Withheld missing secret.local.md: %v", i, b.Withheld)
		}
	}
}

// TestSettingsBundleExcludesLocalOverlay (sty_ea18294f AC2): satelle.toml joins
// the bundle; satelle.local.toml never rides, even with a sentinel secret.
func TestSettingsBundleExcludesLocalOverlay(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".satelle/satelle.toml", "[sync]\nstale_after = \"24h\"\n[hosted]\nproject = \"probe\"\n")
	const secret = "SUPER_SECRET_TOKEN_XYZ"
	writeFile(t, repo, ".satelle/satelle.local.toml", "[vars]\nAPI_KEY = \""+secret+"\"\n")
	for _, scope := range []Scope{PersonalScope, TeamScope} {
		b, err := ConfigFiles(Config{}, scope, repo, Ignore{})
		if err != nil {
			t.Fatalf("ConfigFiles: %v", err)
		}
		if _, ok := findConfigFile(b.Files, "satelle.toml"); !ok {
			t.Error("satelle.toml missing from Files")
		}
		for _, f := range b.Files {
			if f.Path == "satelle.local.toml" {
				t.Error("satelle.local.toml must not be in Files")
			}
			if strings.Contains(string(f.Content), secret) {
				t.Errorf("secret leaked in %s content", f.Path)
			}
			// [hosted] project must be redacted at push
			if f.Path == "satelle.toml" && strings.Contains(string(f.Content), `project = "probe"`) {
				t.Error("satelle.toml content still carries [hosted] project after redact")
			}
		}
		// Overlay may be listed as withheld only if walked — it is not in any area location.
		for _, w := range b.Withheld {
			if w == "satelle.local.toml" {
				// fine if reported; not required
			}
		}
	}
}

// TestLocalOnlyPathSettingsOverlay pins the .local rule for the overlay path.
func TestLocalOnlyPathSettingsOverlay(t *testing.T) {
	if !LocalOnlyPath("satelle.local.toml") {
		t.Error("LocalOnlyPath(satelle.local.toml) = false, want true")
	}
}

// TestStripRepoBindingWithholdsProject: only the settings area loses its project
// binding and any removed [sync] scope key, and what was withheld is reported
// (AC6: a push never carries a scope).
func TestStripRepoBindingWithholdsProject(t *testing.T) {
	body := []byte("[sync]\nall = \"personal\"\nproject = \"from-sync\"\nskills = \"local\"\nstale_after = \"24h\"\n[hosted]\nproject = \"alpha\"\nserver = \"https://x\"\n")
	gotB, withheld := stripRepoBinding("settings", body)
	if strings.Join(withheld, ",") != "[sync] project,[hosted] project,[sync] all,[sync] skills" {
		t.Errorf("withheld = %v", withheld)
	}
	got := string(gotB)
	if strings.Contains(got, `project = "alpha"`) || strings.Contains(got, `project = "from-sync"`) {
		t.Errorf("project not stripped: %s", got)
	}
	if strings.Contains(got, `all = `) || strings.Contains(got, `skills = `) {
		t.Errorf("scope keys not stripped: %s", got)
	}
	if !strings.Contains(got, `stale_after = "24h"`) {
		t.Errorf("sync lost: %s", got)
	}
	// other areas unchanged, nothing withheld
	other, withheld := stripRepoBinding("skills", body)
	if string(other) != string(body) || len(withheld) != 0 {
		t.Error("non-settings area was altered")
	}
	// a settings file with no project binding withholds nothing
	clean := []byte("[sync]\nstale_after = \"24h\"\n")
	if same, withheld := stripRepoBinding("settings", clean); string(same) != string(clean) || len(withheld) != 0 {
		t.Errorf("a settings file with no project binding must pass untouched: %q withheld %v", same, withheld)
	}
}

// TestStripRepoBindingLeavesAgentsAlone (sty_89b9df20): the agents layer travels
// byte-identical — machine-specific content in it is refused at push, never
// rewritten on the way out.
func TestStripRepoBindingLeavesAgentsAlone(t *testing.T) {
	body := []byte("# authored\n[reviewer]\nrole = \"reviewer\"\nprofile = \"claude-opus\"\ncommand = \"/opt/local/bin/claude -p --append-system-prompt {system}\"\nenv = { ANTHROPIC_AUTH_TOKEN = \"sk-literal-secret\" }\n")
	got, withheld := stripRepoBinding("agents", body)
	if string(got) != string(body) || len(withheld) != 0 {
		t.Fatalf("agents area must pass through unchanged:\n%s\nwithheld %v", got, withheld)
	}
}

// The pull-only mark is read from satelle.local.toml alone: a committed
// satelle.toml carrying it marks nothing, and the key is never an area.
func TestPullOnlyReadsLocalFileOnly(t *testing.T) {
	repo := t.TempDir()
	if on, err := PullOnly(repo); err != nil || on {
		t.Fatalf("no local file: PullOnly = %v, %v; want false, nil", on, err)
	}

	writeFile(t, repo, ".satelle/satelle.toml", "[sync]\nproject = \"p\"\npull_only = true\n")
	if on, err := PullOnly(repo); err != nil || on {
		t.Errorf("committed pull_only only: PullOnly = %v, %v; want false, nil", on, err)
	}
	cfg, _, err := Load(filepath.Join(repo, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reservedSyncKeys[syncPullOnlyKey] {
		t.Error("pull_only must be a reserved [sync] key")
	}
	for _, a := range SyncAreas {
		if a == syncPullOnlyKey {
			t.Error("pull_only minted as a sync area")
		}
	}
	if isRemovedSyncScopeKey(syncPullOnlyKey) {
		t.Error("pull_only must not be a removed [sync] scope key")
	}
	if cfg.SyncProject() != "p" {
		t.Errorf("SyncProject = %q beside pull_only; want p", cfg.SyncProject())
	}

	writeFile(t, repo, ".satelle/satelle.local.toml", "[sync]\npull_only = true\n")
	if on, err := PullOnly(repo); err != nil || !on {
		t.Errorf("local pull_only = true: PullOnly = %v, %v; want true, nil", on, err)
	}
	writeFile(t, repo, ".satelle/satelle.local.toml", "[sync]\npull_only = false\n")
	if on, err := PullOnly(repo); err != nil || on {
		t.Errorf("local pull_only = false: PullOnly = %v, %v; want false, nil", on, err)
	}
	writeFile(t, repo, ".satelle/satelle.local.toml", "[sync]\npull_only = \"yes\"\n")
	if _, err := PullOnly(repo); err == nil || !strings.Contains(err.Error(), "pull_only") {
		t.Errorf("non-boolean local pull_only: err = %v, want a config error naming pull_only", err)
	}
}

// PullOnlyEdit writes into the [sync] table the reader reads.
func TestPullOnlyEditRoundTrips(t *testing.T) {
	repo := t.TempDir()
	for _, on := range []bool{true, false} {
		if err := SaveConfigValues(LocalConfigPath(repo), []KeyEdit{PullOnlyEdit(on)}); err != nil {
			t.Fatal(err)
		}
		if got, err := PullOnly(repo); err != nil || got != on {
			t.Errorf("after PullOnlyEdit(%v): PullOnly = %v, %v", on, got, err)
		}
	}
}
