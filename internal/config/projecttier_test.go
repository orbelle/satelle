package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

const tierBoundToml = "[hosted]\nproject = \"probe\"\n"

// tierFixture is a repo whose satelle.toml is satelleToml, with the files of
// tree written under its data dir.
type tierFixture struct {
	root, dataDir, runtimeDir string
	cfg                       Config
}

func newTierFixture(t *testing.T, satelleToml string, tree map[string]string) tierFixture {
	t.Helper()
	testutil.IsolateHome(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, DefaultDataDir)
	writeTierFile(t, filepath.Join(dataDir, ConfigName), satelleToml)
	for rel, body := range tree {
		writeTierFile(t, filepath.Join(dataDir, filepath.FromSlash(rel)), body)
	}
	cfg, _, err := Load(filepath.Join(dataDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	return tierFixture{root: root, dataDir: dataDir, runtimeDir: cfg.ResolveRuntimeDir(root).Dir, cfg: cfg}
}

// deploy writes a project tier for project "probe" holding files.
func (f tierFixture) deploy(t *testing.T, teamPaths []string, files map[string]string) {
	t.Helper()
	m := map[string][]byte{}
	for rel, body := range files {
		m[rel] = []byte(body)
	}
	if err := WriteProjectTier(f.runtimeDir, ProjectTierStamp{Project: "probe", TeamPaths: teamPaths}, m); err != nil {
		t.Fatal(err)
	}
}

func writeTierFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	repoAgentsBody = "[reviewer]\nrole = \"reviewer\"\nmodel = \"repo-copy\"\n\n[repoonly]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
	tierAgentsBody = "[reviewer]\nrole = \"reviewer\"\nmodel = \"team-v1\"\n"
	wsAgentsBody   = "[ghost]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
)

// assertResolvesAsBefore pins every resolution surface of f to what it was
// with no project tier: the repo's dirs alone, the repo and workspace agents
// layers, and every path under the tree.
func assertResolvesAsBefore(t *testing.T, f tierFixture) {
	t.Helper()
	if _, ok := LoadProjectTier(f.cfg, f.root, ""); ok {
		t.Fatal("no project tier must load")
	}
	got := ResolveAuthoredRoots(f.cfg, f.root, "")
	if want := (AuthoredRoots{Dirs: f.cfg.ResolveAuthoredDirs(f.root)}); !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAuthoredRoots = %+v, want %+v", got, want)
	}
	layers, err := LoadAgentLayers(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := LoadAgents(f.dataDir)
	ws, _ := LoadWorkspaceAgents(f.dataDir)
	if layers.ProjectTier != "" || !reflect.DeepEqual(layers.Repo, repo) || !reflect.DeepEqual(layers.Workspace, ws) {
		t.Errorf("agent layers changed: %+v", layers)
	}
	for _, rel := range []string{AgentsRel, "skills/s.md", "scripts/reviewer-token.sh", "principles/p.md"} {
		if got, want := ResolveTeamPath(f.root, rel), filepath.Join(f.dataDir, filepath.FromSlash(rel)); got != want {
			t.Errorf("ResolveTeamPath(%s) = %s, want %s", rel, got, want)
		}
	}
}

// AC4: an unbound repo resolves exactly as before, even beside a tier a
// previous binding left in its runtime dir.
func TestProjectTierUnboundRepoResolvesAsBefore(t *testing.T) {
	f := newTierFixture(t, "", map[string]string{AgentsRel: repoAgentsBody, WorkspaceAgentsRel: wsAgentsBody, "skills/s.md": "repo\n"})
	f.deploy(t, nil, map[string]string{AgentsRel: tierAgentsBody, "skills/s.md": "tier\n"})
	assertResolvesAsBefore(t, f)
	eff, err := LoadEffectiveAgents(f.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Agents.Reviewer.Model != "repo-copy" {
		t.Errorf("unbound repo must keep its own agents.toml: %+v", eff.Agents.Reviewer)
	}
	if _, ok := eff.Agents.Agents["ghost"]; !ok {
		t.Error("unbound repo must keep reading agents.workspace.toml")
	}
}

// AC4: a bound project whose deployed version holds no team-owned file has no
// tier — none is created — and resolves exactly as before.
func TestProjectTierBoundEmptyStoreResolvesAsBefore(t *testing.T) {
	f := newTierFixture(t, tierBoundToml, map[string]string{AgentsRel: repoAgentsBody, WorkspaceAgentsRel: wsAgentsBody, "skills/s.md": "repo\n"})
	f.deploy(t, nil, nil)
	if _, err := os.Stat(ProjectTierDir(f.runtimeDir)); !os.IsNotExist(err) {
		t.Fatalf("an empty store must create no tier dir: %v", err)
	}
	assertResolvesAsBefore(t, f)

	// A store holding only files the team does not own is the same: no tier.
	f.deploy(t, nil, map[string]string{"principles/p.md": "team principle\n"})
	assertResolvesAsBefore(t, f)
}

// AC1: with the tier present, each team-owned item resolves from it, the
// repo's copy is not a layer, and a repo-only seat or a stale workspace layer
// cannot govern.
func TestProjectTierTeamOwnedResolvesFromTier(t *testing.T) {
	f := newTierFixture(t, tierBoundToml, map[string]string{
		AgentsRel: repoAgentsBody, WorkspaceAgentsRel: wsAgentsBody, "skills/s.md": "repo\n", "skills/repo-only.md": "extra\n",
	})
	f.deploy(t, nil, map[string]string{AgentsRel: tierAgentsBody, "skills/s.md": "tier\n"})
	tierDir := ProjectTierDir(f.runtimeDir)

	roots := ResolveAuthoredRoots(f.cfg, f.root, "")
	if roots.Tier != tierDir || !reflect.DeepEqual(roots.Team, NewTeamSet(nil).Globs()) {
		t.Fatalf("roots = %+v", roots)
	}
	if !roots.Owns("skills/repo-only.md") {
		t.Error("a repo-only file under a team glob is replaced with the set")
	}
	if got := ResolveTeamPath(f.root, "skills/s.md"); got != filepath.Join(tierDir, "skills", "s.md") {
		t.Errorf("ResolveTeamPath = %s", got)
	}
	layers, err := LoadAgentLayers(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if layers.ProjectTier != filepath.Join(tierDir, filepath.FromSlash(AgentsRel)) {
		t.Errorf("agents Repo layer must be the tier's: %q", layers.ProjectTier)
	}
	eff, err := LoadEffectiveAgents(f.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Agents.Reviewer.Model != "team-v1" {
		t.Errorf("reviewer model = %q, want the deployed team-v1", eff.Agents.Reviewer.Model)
	}
	for _, seat := range []string{"repoonly", "ghost"} {
		if _, ok := eff.Agents.Agents[seat]; ok {
			t.Errorf("seat %q (repo copy / workspace layer) must not govern", seat)
		}
	}
}

// AC2: a path outside the team set resolves from the tree with the tier present,
// and the default set is agents.toml, workflows/**, skills/**.
func TestProjectTierUnlistedPathsResolveFromTree(t *testing.T) {
	def := NewTeamSet(nil)
	for rel, want := range map[string]bool{
		AgentsRel: true, "agents.toml": true, "workflows/done.toml": true, "workflows/sub/x.md": true, "skills/a.md": true,
		"principles/p.md": false, "constitution.md": false, ConfigName: false, "scripts/x.sh": false, "hooks/h.sh": false,
	} {
		if got := def.Owns(rel); got != want {
			t.Errorf("default set Owns(%s) = %v, want %v", rel, got, want)
		}
	}
	f := newTierFixture(t, tierBoundToml, map[string]string{"principles/p.md": "local principle\n"})
	f.deploy(t, nil, map[string]string{"skills/s.md": "tier\n", "principles/p.md": "team principle\n"})
	if got, want := ResolveTeamPath(f.root, "principles/p.md"), filepath.Join(f.dataDir, "principles", "p.md"); got != want {
		t.Errorf("unlisted principle resolves to %s, want the tree's %s", got, want)
	}
	if ResolveAuthoredRoots(f.cfg, f.root, "").Owns("principles/p.md") {
		t.Error("principles are not team-owned by default")
	}
}

// AC3: agents.local.toml and satelle.local.toml sit above the tier; neither a
// .local file nor satelle.toml can ever be team-owned.
func TestProjectTierLocalFilesTakePrecedence(t *testing.T) {
	f := newTierFixture(t, tierBoundToml, map[string]string{
		AgentsRel: repoAgentsBody, LocalAgentsRel: "[reviewer]\nmodel = \"mine\"\n",
	})
	f.deploy(t, nil, map[string]string{AgentsRel: tierAgentsBody, "skills/s.md": "tier\n"})
	eff, err := LoadEffectiveAgents(f.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Agents.Reviewer.Model != "mine" || eff.Provenance.Source("reviewer", "model") != SourceLocal {
		t.Errorf("agents.local.toml must beat the tier: %q (%s)", eff.Agents.Reviewer.Model, eff.Provenance.Source("reviewer", "model"))
	}

	all := NewTeamSet([]string{"**"})
	for _, rel := range []string{LocalAgentsRel, LocalConfigName, ConfigName, WorkspaceAgentsRel, "skills/x.local.md"} {
		if all.Owns(rel) {
			t.Errorf("%s must never be team-owned", rel)
		}
	}

	// satelle.local.toml's [team] overrides the deployed set: narrowed to skills,
	// agents.toml leaves the tier; emptied, the tier owns nothing.
	writeTierFile(t, filepath.Join(f.dataDir, LocalConfigName), "[team]\npaths = [\"skills/**\"]\n")
	if _, ok := ProjectTierAgentsPath(f.dataDir); ok {
		t.Error("satelle.local.toml [team] must narrow the set")
	}
	if got := ResolveTeamPath(f.root, "skills/s.md"); got != filepath.Join(ProjectTierDir(f.runtimeDir), "skills", "s.md") {
		t.Errorf("skills stay team-owned: %s", got)
	}
	writeTierFile(t, filepath.Join(f.dataDir, LocalConfigName), "[team]\npaths = []\n")
	if _, ok := LoadProjectTier(f.cfg, f.root, ""); ok {
		t.Error("an empty local [team] owns nothing, so there is no tier")
	}
}

// AC6: the set can name scripts and hooks outside every config area; they
// resolve from the tier, while an init-owned hook stays with the tree.
func TestProjectTierScriptsAndHooks(t *testing.T) {
	team := []string{"scripts/*.sh", "hooks/*"}
	ts := NewTeamSet(team)
	if !ts.Owns("scripts/reviewer-token.sh") || !ts.Owns("hooks/pr-review-post.sh") {
		t.Fatal("scripts and hooks globs must own their files")
	}
	if ts.Owns("hooks/satelle-hook.sh") {
		t.Error("hooks/satelle-hook.sh is init-owned")
	}
	if got := ts.InitOwnedMatched(); len(got) == 0 || got[0] != "hooks/satelle-hook.sh" {
		t.Errorf("InitOwnedMatched = %v", got)
	}
	f := newTierFixture(t, tierBoundToml, map[string]string{
		"scripts/reviewer-token.sh": "#!/bin/sh\necho drifted\n", "hooks/satelle-hook.sh": "#!/bin/sh\necho init\n",
	})
	f.deploy(t, team, map[string]string{"scripts/reviewer-token.sh": "#!/bin/sh\necho team\n", "hooks/pr-review-post.sh": "#!/bin/sh\necho post\n"})
	tierDir := ProjectTierDir(f.runtimeDir)
	for _, rel := range []string{"scripts/reviewer-token.sh", ".satelle/hooks/pr-review-post.sh"} {
		want := filepath.Join(tierDir, filepath.FromSlash(normaliseTeamGlob(rel)))
		if got := ResolveTeamPath(f.root, rel); got != want {
			t.Errorf("ResolveTeamPath(%s) = %s, want %s", rel, got, want)
		}
		info, err := os.Stat(want)
		if err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("tier script %s must be executable: %v %v", want, info, err)
		}
	}
	if got, want := ResolveTeamPath(f.root, "hooks/satelle-hook.sh"), filepath.Join(f.dataDir, "hooks", "satelle-hook.sh"); got != want {
		t.Errorf("init-owned hook resolves to %s, want the tree's %s", got, want)
	}
}

// AC5/AC6: the project push carries the team-glob files no config area holds,
// labelled as the team area, and never an init-owned file.
func TestConfigFilesCarriesTeamFilesOutsideConfigAreas(t *testing.T) {
	f := newTierFixture(t, "[team]\npaths = [\"agents.toml\", \"skills/**\", \"scripts/*.sh\", \"hooks/*\"]\n"+tierBoundToml, map[string]string{
		"scripts/reviewer-token.sh": "#!/bin/sh\n", "hooks/pr-review-post.sh": "#!/bin/sh\n", "hooks/satelle-hook.sh": "#!/bin/sh\n", "scripts/notes.txt": "x\n",
	})
	b, err := ConfigFiles(f.cfg, PersonalScope, f.root, Ignore{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, cf := range b.Files {
		got[cf.Path] = cf.Area
	}
	for _, rel := range []string{"scripts/reviewer-token.sh", "hooks/pr-review-post.sh"} {
		if got[rel] != TeamArea {
			t.Errorf("%s area = %q, want %q (bundle %v)", rel, got[rel], TeamArea, got)
		}
	}
	for _, rel := range []string{"hooks/satelle-hook.sh", "scripts/notes.txt"} {
		if _, ok := got[rel]; ok {
			t.Errorf("%s must not be pushed", rel)
		}
	}
}

// The write-down record round-trips, survives a deploy, and an absent record
// reads empty.
func TestTeamWrittenRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if got := ReadTeamWritten(dir); len(got) != 0 {
		t.Fatalf("absent record = %v, want empty", got)
	}
	want := map[string]string{AgentsRel: "aa", "skills/s.md": "bb"}
	if err := WriteTeamWritten(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := ReadTeamWritten(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("record = %v, want %v", got, want)
	}
	if err := WriteProjectTier(dir, ProjectTierStamp{Project: "probe"}, map[string][]byte{AgentsRel: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if got := ReadTeamWritten(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("a deploy must keep the record, got %v", got)
	}
}

// A team file starting with "#!" is executable; any other is not.
func TestTeamFileMode(t *testing.T) {
	if m := TeamFileMode([]byte("#!/bin/sh\n")); m != 0o755 {
		t.Errorf("script mode = %o, want 755", m)
	}
	if m := TeamFileMode([]byte("# skill\n")); m != 0o644 {
		t.Errorf("file mode = %o, want 644", m)
	}
}

// A [team] entry that escapes the data dir fails at load.
func TestTeamPathsRefuseEscape(t *testing.T) {
	testutil.IsolateHome(t)
	p := filepath.Join(t.TempDir(), ConfigName)
	writeTierFile(t, p, "[team]\npaths = [\"../outside/*\"]\n")
	if _, _, err := Load(p); err == nil {
		t.Fatal("an escaping [team] path must be refused")
	}
}

// sty_195dd215 AC5: two keys that name one tier path — a hosted copy's legacy bare
// agents.toml beside workflows/agents.toml — are refused before anything is
// written, so the old tier stays whole and no path is listed twice.
func TestWriteProjectTierRefusesCollidingKeys(t *testing.T) {
	dir := t.TempDir()
	if err := WriteProjectTier(dir, ProjectTierStamp{Project: "probe"}, map[string][]byte{AgentsRel: []byte("old tier\n")}); err != nil {
		t.Fatal(err)
	}
	err := WriteProjectTier(dir, ProjectTierStamp{Project: "probe"}, map[string][]byte{
		AgentsConfigName: []byte("legacy\n"),
		AgentsRel:        []byte("current\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "agents.toml and workflows/agents.toml both map to workflows/agents.toml") {
		t.Fatalf("colliding keys: err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(ProjectTierDir(dir), filepath.FromSlash(AgentsRel))); string(got) != "old tier\n" {
		t.Errorf("the old tier was not left whole: %q", got)
	}
}
