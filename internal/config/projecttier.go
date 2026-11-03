package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/docindex"
)

// The project tier (sty_8dedc0a4) is the team-owned files of the bound
// project's deployed hosted version, kept out of the tree under the runtime dir:
// <runtime>/project-tier/<data-dir-relative path>. `satelle sync config deploy`
// writes it whole. In a repo that has one, every path the team set owns resolves
// from it and the repo's copy is not a layer; every other path resolves as
// before. A repo with no tier — unbound, or bound to a store whose deployed
// version holds no team-owned file — resolves exactly as it did without one.
const (
	ProjectTierDirName   = "project-tier"
	ProjectTierStampName = ".project-tier.json"
	// ProjectTierWrittenName records, beside the tier dir and not in it, the
	// sha256 of each team-owned file satelle last wrote into the tree from the
	// tier (sty_4cc971f7), so a deploy's whole-dir swap keeps it and a tree copy
	// that no longer matches it is known to be a local edit.
	ProjectTierWrittenName = "project-tier-written.json"
)

// ProjectTierStamp records what a deploy wrote into the tier.
type ProjectTierStamp struct {
	Project string `json:"project"`
	Server  string `json:"server,omitempty"`
	// Version is the pinned version deployed, 0 for the latest heads.
	Version int `json:"version,omitempty"`
	// TeamPaths is the deployed satelle.toml's [team] paths; nil when it declares
	// none, so the default set applies.
	TeamPaths  []string  `json:"team_paths"`
	Files      []string  `json:"files"`
	DeployedAt time.Time `json:"deployed_at"`
}

// ProjectTier is a loaded tier: its dir, its stamp, and the effective team set
// (satelle.local.toml's [team] over the deployed one).
type ProjectTier struct {
	Dir   string
	Stamp ProjectTierStamp
	Team  TeamSet
}

// Path is the tier location of a data-dir-relative path.
func (t ProjectTier) Path(rel string) string {
	return filepath.Join(t.Dir, filepath.FromSlash(normaliseTeamGlob(rel)))
}

// ProjectTierDir is the tier dir under a runtime dir.
func ProjectTierDir(runtimeDir string) string {
	return filepath.Join(runtimeDir, ProjectTierDirName)
}

// ReadProjectTierStamp reads a tier dir's stamp.
func ReadProjectTierStamp(tierDir string) (ProjectTierStamp, bool) {
	b, err := os.ReadFile(filepath.Join(tierDir, ProjectTierStampName))
	if err != nil {
		return ProjectTierStamp{}, false
	}
	var st ProjectTierStamp
	if json.Unmarshal(b, &st) != nil {
		return ProjectTierStamp{}, false
	}
	return st, true
}

// LoadProjectTier loads the tier of the process of record. runtimeDir is the
// repo's runtime dir; empty resolves it. ok is false — there is no tier —
// unless the config binds a project ([sync] project, or a leftover [hosted]
// project), the stamp names that project, and the tier holds at least one file
// the effective team set owns. An unbound repo returns before any path is
// resolved, so it does no work it did not do before. A legacy runtime dir (still
// the data dir) has no tier: the tier must never sit in the tree.
func LoadProjectTier(process Config, processRoot, runtimeDir string) (ProjectTier, bool) {
	project := process.SyncProject()
	if project == "" {
		return ProjectTier{}, false
	}
	dataDir := process.ResolveDataDir(processRoot)
	if strings.TrimSpace(runtimeDir) == "" {
		res := process.ResolveRuntimeDir(processRoot)
		if res.Legacy {
			return ProjectTier{}, false
		}
		runtimeDir = res.Dir
	}
	if sameResolvedPath(runtimeDir, dataDir) {
		return ProjectTier{}, false
	}
	dir := ProjectTierDir(runtimeDir)
	st, ok := ReadProjectTierStamp(dir)
	if !ok || st.Project != project {
		return ProjectTier{}, false
	}
	paths := st.TeamPaths
	if local, ok := LocalTeamPaths(dataDir); ok {
		paths = local
	}
	tier := ProjectTier{Dir: dir, Stamp: st, Team: NewTeamSet(paths)}
	for _, f := range st.Files {
		if tier.Team.Owns(f) && fileExists(tier.Path(f)) {
			return tier, true
		}
	}
	return ProjectTier{}, false
}

// ProjectTierOf loads the tier of the repo whose data dir is dataDir, for the
// readers that hold only a data dir (the agents layers, doctor's disk readers).
func ProjectTierOf(dataDir string) (ProjectTier, bool) {
	dataDir = filepath.Clean(dataDir)
	root := filepath.Dir(dataDir)
	var cfg Config
	if p := filepath.Join(dataDir, ConfigName); fileExists(p) {
		if c, _, err := Load(p); err == nil {
			cfg = c
		}
	}
	return LoadProjectTier(cfg, root, "")
}

// ProjectTierAgentsPath is the tier's agents layer when the team owns
// agents.toml and the tier is present. That file then replaces the repo's
// agents.toml, and the synced workspace layer is not read.
func ProjectTierAgentsPath(dataDir string) (string, bool) {
	tier, ok := ProjectTierOf(dataDir)
	if !ok || !tier.Team.Owns(AgentsRel) {
		return "", false
	}
	return tier.Path(AgentsRel), true
}

// AuthoredRoots is where each authored kind resolves from: the repo's per-kind
// dirs and, with a project tier, the tier root and the team globs whose paths it
// replaces. It is the one owner of authored-root resolution.
type AuthoredRoots struct {
	Dirs map[string]string `json:"dirs"`
	Tier string            `json:"tier,omitempty"`
	Team []string          `json:"team,omitempty"`
}

// Owns reports whether a data-dir-relative path resolves from the tier.
func (r AuthoredRoots) Owns(rel string) bool {
	return strings.TrimSpace(r.Tier) != "" && NewTeamSet(r.teamGlobs()).Owns(rel)
}

// teamGlobs is Team with nil read as "owns nothing": AuthoredRoots carries the
// effective set, never the default placeholder.
func (r AuthoredRoots) teamGlobs() []string {
	if r.Team == nil {
		return []string{}
	}
	return r.Team
}

// Index is r as the document index reads it.
func (r AuthoredRoots) Index() docindex.Roots {
	out := docindex.Roots{Dirs: r.Dirs}
	if strings.TrimSpace(r.Tier) != "" {
		ts := NewTeamSet(r.teamGlobs())
		out.Tier, out.Owned = r.Tier, ts.Owns
	}
	return out
}

// WorkflowsDir is the workflows dir that governs: the tier's when the team owns
// the route, else the repo's.
func (r AuthoredRoots) WorkflowsDir() string {
	if r.Owns("workflows/done.toml") {
		return r.Index().TierDir("workflows")
	}
	return r.Dirs["workflows"]
}

// AuthoredRootsOf is ResolveAuthoredRoots for a reader that holds only a data
// dir: <dataDir>/<kind> for each kind, plus that repo's project tier.
func AuthoredRootsOf(dataDir string) AuthoredRoots {
	r := AuthoredRoots{Dirs: make(map[string]string, len(AuthoredKinds))}
	for _, kind := range AuthoredKinds {
		r.Dirs[kind] = filepath.Join(dataDir, kind)
	}
	if tier, ok := ProjectTierOf(dataDir); ok {
		r.Tier, r.Team = tier.Dir, tier.Team.Globs()
	}
	return r
}

// ResolveAuthoredRoots resolves the authored roots of the process of record:
// [substrate_roots] over <data_dir>/<kind> for each kind, anchored at
// processRoot, plus the project tier when the repo has one. runtimeDir is the
// repo's runtime dir; empty resolves it.
func ResolveAuthoredRoots(process Config, processRoot, runtimeDir string) AuthoredRoots {
	r := AuthoredRoots{Dirs: process.ResolveAuthoredDirs(processRoot)}
	if tier, ok := LoadProjectTier(process, processRoot, runtimeDir); ok {
		r.Tier, r.Team = tier.Dir, tier.Team.Globs()
	}
	return r
}

// ResolveTeamPath is where a data-dir-relative path resolves for the repo at
// repoRoot: the project tier's copy when the tier is present and the team owns
// rel, else the repo's own <data dir>/<rel>. `satelle team path` prints it, so
// any command runner can reach a team script with no environment of its own.
func ResolveTeamPath(repoRoot, rel string) string {
	if abs, err := filepath.Abs(repoRoot); err == nil {
		repoRoot = abs
	}
	rel = normaliseTeamGlob(rel)
	process, processRoot, _, err := LoadRepoProcess(repoRoot)
	if err == nil {
		if tier, ok := LoadProjectTier(process, processRoot, ""); ok && tier.Team.Owns(rel) {
			return tier.Path(rel)
		}
	}
	loc, _, lerr := Load(filepath.Join(repoRoot, DefaultDataDir, ConfigName))
	if lerr != nil {
		loc = Config{}
	}
	return filepath.Join(loc.ResolveDataDir(repoRoot), filepath.FromSlash(rel))
}

// TeamFileMode is the mode a team-owned file is written with, in the tier and in
// the tree: executable when its bytes start with "#!", so a team script runs.
func TeamFileMode(body []byte) os.FileMode {
	if bytes.HasPrefix(body, []byte("#!")) {
		return 0o755
	}
	return 0o644
}

// ReadTeamWritten reads the record of what was last written into the tree from
// the tier: data-dir-relative path to sha256 hex. Absent or unreadable is empty.
func ReadTeamWritten(runtimeDir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(runtimeDir, ProjectTierWrittenName))
	if err != nil {
		return out
	}
	if json.Unmarshal(b, &out) != nil || out == nil {
		return map[string]string{}
	}
	return out
}

// WriteTeamWritten replaces that record.
func WriteTeamWritten(runtimeDir string, written map[string]string) error {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("project tier: %w", err)
	}
	b, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, ProjectTierWrittenName), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("project tier: write record: %w", err)
	}
	return nil
}

// WriteProjectTier replaces the tier under runtimeDir with files (keyed by
// data-dir-relative path) and its stamp. The new tier is written beside the old
// and swapped in, so a failed write leaves the old tier whole. With no file the
// team owns, any old tier is removed and none is created. A file whose bytes
// start with "#!" is written executable, so a team script runs from the tier.
func WriteProjectTier(runtimeDir string, stamp ProjectTierStamp, files map[string][]byte) error {
	dir := ProjectTierDir(runtimeDir)
	if len(files) == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("project tier: remove %s: %w", dir, err)
		}
		return nil
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("project tier: %w", err)
	}
	tmp, err := os.MkdirTemp(runtimeDir, ProjectTierDirName+".new-")
	if err != nil {
		return fmt.Errorf("project tier: %w", err)
	}
	defer os.RemoveAll(tmp)
	// Two keys that name one tier path would race for it in map order and list it
	// twice in the stamp: refuse before anything is written, so the old tier stays.
	keyOf := make(map[string]string, len(files))
	for key := range files {
		rel := normaliseTeamGlob(key)
		if other, dup := keyOf[rel]; dup {
			a, b := other, key
			if b < a {
				a, b = b, a
			}
			return fmt.Errorf("project tier: %s and %s both map to %s", a, b, rel)
		}
		keyOf[rel] = key
	}
	stamp.Files = stamp.Files[:0]
	for rel, body := range files {
		rel = normaliseTeamGlob(rel)
		dest := filepath.Join(tmp, filepath.FromSlash(rel))
		if !strings.HasPrefix(dest, tmp+string(filepath.Separator)) {
			return fmt.Errorf("project tier: %s escapes the tier", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("project tier: %w", err)
		}
		if err := os.WriteFile(dest, body, TeamFileMode(body)); err != nil {
			return fmt.Errorf("project tier: write %s: %w", rel, err)
		}
		stamp.Files = append(stamp.Files, rel)
	}
	sort.Strings(stamp.Files)
	if stamp.DeployedAt.IsZero() {
		stamp.DeployedAt = time.Now().UTC()
	}
	sb, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ProjectTierStampName), append(sb, '\n'), 0o644); err != nil {
		return fmt.Errorf("project tier: write stamp: %w", err)
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("project tier: %w", err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir)
		return fmt.Errorf("project tier: %w", err)
	}
	return os.RemoveAll(old)
}
