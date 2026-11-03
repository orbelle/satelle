package config

// Sync walks and the scope ladder — PURE CONFIG: no network, no git
// side-effects. The scope is one value per repo, resolved from the signed-in
// account and the repo's [sync] project binding by internal/syncscope; this
// package only walks the areas a resolved scope moves and labels their tiers.
// The repo holds the binding alone: per-area [sync] scope keys are refused
// (RemovedSyncScopeKeys).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Scope is a position on the local -> personal -> team sync ladder: where a
// repo's .satelle areas sync to, as resolved from the account and the binding.
type Scope int

const (
	// LocalScope never leaves this machine: unsigned, unbound, or bound to a
	// project whose team the account is not in.
	LocalScope Scope = iota
	// PersonalScope syncs to the same operator's other machines: a project of
	// the account's personal team.
	PersonalScope
	// TeamScope syncs to the whole team: a project of a team the account is a
	// member of.
	TeamScope
)

// String renders the scope as its recorded word.
func (s Scope) String() string {
	switch s {
	case PersonalScope:
		return "personal"
	case TeamScope:
		return "team"
	default:
		return "local"
	}
}

// ParseScope parses a recorded scope word off the local|personal|team ladder.
// An unrecognised value is an error rather than a silent local.
func ParseScope(s string) (Scope, error) {
	switch s {
	case "local":
		return LocalScope, nil
	case "personal":
		return PersonalScope, nil
	case "team":
		return TeamScope, nil
	default:
		return LocalScope, fmt.Errorf("sync: unknown scope %q (want local|personal|team)", s)
	}
}

// SyncAreas is the canonical, de-duplicated list of .satelle areas a sync
// walks: the AuthoredKinds (documents/workflows/principles/skills) plus the
// project constitution, the agents layer, tasks, settings, and the work-state
// areas (stories, ledger, executions). Every one moves under a non-local
// resolved scope; none is a config key.
var SyncAreas = buildSyncAreas()

// RemovedSyncScopeKeys are the [sync] keys that once set a scope per area (and
// "all" for every area). Scope comes from the account's project binding now, so
// loading a file carrying one fails, satelle init removes them
// (FindSyncScopeKeys), and a settings push withholds them.
var RemovedSyncScopeKeys = append([]string{"all"}, SyncAreas...)

// RemovedRecentWindowKey is the [sync] duration that once kept a story's full
// history in hosted mode after its newest event. A team-scope store now holds a
// story's history while it is performing, and personal and local scope hold every
// chain (sty_7c01ca14), so it is refused and removed like the scope keys.
const RemovedRecentWindowKey = "recent_window"

// RemovedSyncKeys is every removed [sync] key: RemovedSyncScopeKeys and
// RemovedRecentWindowKey. Loading a file carrying one fails, satelle init removes
// them (FindSyncScopeKeys, RemoveSyncScopeKeys), and a settings push withholds them.
var RemovedSyncKeys = append(append([]string{}, RemovedSyncScopeKeys...), RemovedRecentWindowKey)

// syncScopeHeal is the command that removes RemovedSyncKeys from a tree.
const syncScopeHeal = "satelle init"

// RemovedSyncScopeKeyError is the refusal for one removed [sync] key, naming why
// it went and the heal.
func RemovedSyncScopeKeyError(key string) error {
	why := "sync scope now comes from the account's project binding"
	if key == RemovedRecentWindowKey {
		why = "a team-scope store holds a story's history while it is performing, and personal and local scope keep every chain"
	}
	return fmt.Errorf("sync: [sync] %s is removed — %s; run %q to remove it", key, why, syncScopeHeal)
}

func isRemovedSyncScopeKey(k string) bool {
	for _, r := range RemovedSyncKeys {
		if r == k {
			return true
		}
	}
	return false
}

// FindSyncScopeKeys reports, per file name (ConfigName, LocalConfigName) under
// dataDir, the RemovedSyncKeys its [sync] table carries, sorted. It decodes
// each file as plain TOML — never through Config, so the refusal cannot stop it
// — and a missing file contributes nothing.
func FindSyncScopeKeys(dataDir string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, name := range []string{ConfigName, LocalConfigName} {
		path := filepath.Join(dataDir, name)
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var doc map[string]any
		if err := toml.Unmarshal(b, &doc); err != nil {
			return nil, &ParseError{Path: path, Err: err}
		}
		table, _ := doc["sync"].(map[string]any)
		var keys []string
		for k := range table {
			if isRemovedSyncScopeKey(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			out[name] = keys
		}
	}
	return out, nil
}

// RemoveSyncScopeKeys removes every RemovedSyncKeys key from a TOML document's
// [sync] table, returning the edited text and the keys removed in RemovedSyncKeys
// order. Every other line is kept as it was.
func RemoveSyncScopeKeys(s string) (string, []string) {
	var removed []string
	for _, k := range RemovedSyncKeys {
		if next := RemoveKey(s, "sync", k); next != s {
			removed = append(removed, k)
			s = next
		}
	}
	return s, removed
}

// syncStaleAfterKey is the [sync] duration after which an opted-in workstate
// partition is unbacked (sty_30696eeb). Reserved so it is never treated as an area.
const syncStaleAfterKey = "stale_after"

// syncHoldUnpushedKey is the [sync] switch for the work-state push hold
// (sty_7361569a): an in-flight story whose code is not on the git remote is
// not published. Reserved so it is never treated as an area.
const syncHoldUnpushedKey = "hold_unpushed"

// syncPullOnlyKey is the [sync] switch that makes this machine pull from its
// bound project and never push to it (sty_07eefabe). It is machine state: only
// PullOnly reads it, from satelle.local.toml alone. Reserved so it is never
// treated as an area.
const syncPullOnlyKey = "pull_only"

// WorkstateAreas are the [sync] areas that form the work-state kind.
var WorkstateAreas = []string{"stories", "executions", "ledger"}

// reservedSyncKeys are the [sync] connection and switch keys; they must never
// be minted as SyncAreas.
var reservedSyncKeys = map[string]bool{
	syncServerKey: true, syncProjectKey: true, syncWorkspaceKey: true,
	syncStaleAfterKey: true, syncHoldUnpushedKey: true, syncPullOnlyKey: true,
}

// SyncTable is the [sync] table: the connection and switch keys. Values are
// strings; a bare TOML boolean is accepted and kept as "true"/"false" so a
// switch reads naturally (hold_unpushed = false). Decoding merges into an
// existing table, so the satelle.local.toml overlay wins per key. A
// RemovedSyncKeys key is refused.
type SyncTable map[string]string

// UnmarshalTOML implements toml.Unmarshaler.
func (t *SyncTable) UnmarshalTOML(data any) error {
	m, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf("sync: [sync] must be a table, got %T", data)
	}
	if *t == nil {
		*t = SyncTable{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if isRemovedSyncScopeKey(k) {
			return RemovedSyncScopeKeyError(k)
		}
	}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			(*t)[k] = x
		case bool:
			(*t)[k] = strconv.FormatBool(x)
		default:
			return fmt.Errorf("sync: %s must be a string or boolean, got %T", k, v)
		}
	}
	return nil
}

// HoldUnpushed reads [sync] hold_unpushed: whether work-state push holds back a
// story whose code is not yet on the git remote. Missing means true; a value
// that is not a boolean is a config error.
func HoldUnpushed(cfg Config) (bool, error) {
	raw := strings.TrimSpace(cfg.Sync[syncHoldUnpushedKey])
	if raw == "" {
		return true, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("sync: hold_unpushed %q is not a boolean (want true|false)", raw)
	}
	return b, nil
}

// PullOnly reads [sync] pull_only from the repo's satelle.local.toml only: the
// committed and hosted satelle.toml never mark a machine pull-only, so the mark
// cannot travel as repo config. An absent file or key is false; a value that is
// not a boolean is a config error.
func PullOnly(repoRoot string) (bool, error) {
	path := LocalConfigPath(repoRoot)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: read %s: %w", path, err)
	}
	var local struct {
		Sync map[string]any `toml:"sync"`
	}
	if _, err := toml.Decode(string(b), &local); err != nil {
		return false, &ParseError{Path: path, Err: err}
	}
	v, ok := local.Sync[syncPullOnlyKey]
	if !ok {
		return false, nil
	}
	on, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("sync: pull_only in %s is %v, not a boolean (want true|false)", path, v)
	}
	return on, nil
}

// LocalConfigPath is the repo's gitignored satelle.local.toml.
func LocalConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, DefaultDataDir, LocalConfigName)
}

// PullOnlyEdit is the single write of the pull-only mark, into LocalConfigPath.
func PullOnlyEdit(on bool) KeyEdit {
	return KeyEdit{Section: "sync", Key: syncPullOnlyKey, Value: strconv.FormatBool(on)}
}

// WorkstateStaleAfter reads [sync] stale_after. ok=false when the key is
// absent. An unparseable or non-positive value is a config error.
func WorkstateStaleAfter(cfg Config) (time.Duration, bool, error) {
	raw := strings.TrimSpace(cfg.Sync[syncStaleAfterKey])
	if raw == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false, fmt.Errorf("sync: stale_after %q is not a duration: %w", raw, err)
	}
	if d <= 0 {
		return 0, false, fmt.Errorf("sync: stale_after must be positive, got %s", d)
	}
	return d, true, nil
}

func buildSyncAreas() []string {
	out := make([]string, 0, len(AuthoredKinds)+7)
	out = append(out, AuthoredKinds...)
	// settings = satelle.toml, pushed less its binding and any removed scope
	// key (stripRepoBinding). Deploy materialises whatever the manifest holds,
	// so a wiped tree can always get its satelle.toml back.
	return append(out, "constitution", "agents", "tasks", "settings", "stories", "ledger", "executions")
}

// sharedFrontmatterKey is the OKF frontmatter key marking an individual file,
// inside a personal-scope area, for promotion to the shared tier.
const sharedFrontmatterKey = "shared"

// FileShared reports whether a file's frontmatter marks it shared. frontmatter
// is the file's raw body (or just its leading `---`-fenced YAML block) — either
// works, since only the leading fence is scanned. The flag is only meaningful
// under a personal scope: local and team scopes apply uniformly to every file,
// so any other scope reports false without inspecting frontmatter at all.
func FileShared(scope Scope, frontmatter string) bool {
	if scope != PersonalScope {
		return false
	}
	return fmBoolScalar(frontmatter, sharedFrontmatterKey)
}

// ConfigAreas are the .satelle areas eligible for the versioned config store
// (epic:scoped-sync, order:5): the authored kinds MINUS documents (which is its
// own sync kind), plus the project constitution, the agents layer, and task
// DEFINITIONS. The work-state areas (stories/ledger/executions) are order:7;
// documents is its own kind. Under a local scope nothing is walked — this list
// is the candidate set the walk resolves a tier for.
var ConfigAreas = []string{"workflows", "principles", "skills", "constitution", "agents", "tasks", "settings"}

// SubstrateAreas are the areas whose files a sync under scope would move to the
// hosted copy: the config areas and documents, or none under a local scope.
// Everything that reports on substrate continuity takes its areas here.
func SubstrateAreas(scope Scope) []string {
	if scope == LocalScope {
		return nil
	}
	return append(append([]string{}, ConfigAreas...), "documents")
}

// ConfigTier flags whether a file is eligible to be exposed via satelle publish
// (SharedTier) or stays purely personal (PersonalTier). It is NOT a sync push
// destination — satelle sync always writes to the personal workspace; Tier ==
// SharedTier only triggers an operator note (epic:sync-publish). A LocalScope
// area never reaches tier resolution — it is skipped wholesale before any file
// is read, so there is no LocalTier.
type ConfigTier int

const (
	// PersonalTier is ordinary personal-scope content (sync destination: personal).
	PersonalTier ConfigTier = iota
	// SharedTier marks a team scope or frontmatter shared:true. Sync still pushes
	// to the bound project only; the CLI emits a note pointing operators at
	// satelle publish for team catalog exposure (not dual-destination).
	SharedTier
)

// String renders the tier as its push/deploy label.
func (t ConfigTier) String() string {
	if t == SharedTier {
		return "shared"
	}
	return "personal"
}

// ConfigFile is one resolved authored-config file destined for the versioned
// store. Path is server-relative (forward slashes, no leading ".satelle/", e.g.
// "skills/my-skill.md", "agents.toml", "constitution.md", "tasks/tsk_x.md") —
// the key the server stores the file under, stable regardless of where a kind
// lives on disk via [substrate_roots].
type ConfigFile struct {
	Area    string     // the config-area name (skills, constitution, agents, tasks, ...)
	Path    string     // server-relative path under the workspace-config root
	Tier    ConfigTier // PersonalTier | SharedTier (label only; sync dest is personal)
	Content []byte     // the bytes a push sends: the file verbatim, less Withheld
	// Withheld names the satelle.toml keys a push left out of Content because they
	// bind this machine's checkout to a hosted project ("[hosted] project"). Empty
	// for every other file, whose Content is byte-identical to what is on disk.
	Withheld []string
}

// Bundle is an assembled set of files destined for transmission, plus the
// server-relative paths left out so a caller can REPORT them, each under its own
// reason. Work-state push transmits DB rows (no on-disk filename), so it is out
// of scope for these exclusions by deliberate decision — not a gap.
type Bundle struct {
	Files    []ConfigFile
	Withheld []string // sorted, server-relative: never leaves this machine (LocalOnlyPath)
	// UneditedDefaults are the sorted, server-relative paths of files that are
	// unedited embedded defaults (UneditedDefault). The installed binary supplies
	// them, so they are not versioned or synced (sty_097833e8).
	UneditedDefaults []string
}

// ExcludedCount is how many paths the bundle left out, for either reason.
func (b Bundle) ExcludedCount() int { return len(b.Withheld) + len(b.UneditedDefaults) }

// LocalOnlyPath reports whether a path is one that must NEVER be transmitted:
// any path segment carrying a `.local` dot-component (satelle.local.toml,
// notes.local.md, secrets.local/keys.md). Unconditional — it takes no Config,
// so no setting can disable or narrow it. The restore-side counterpart is
// subsync.ExcludedLocal (which delegates here for the .local case).
//
// A bare segment named "local" (skills/local/x.md) is an ordinary name and is
// not withheld; the match requires a multi-component segment whose non-stem
// component equals "local" (case-insensitive).
func LocalOnlyPath(p string) bool {
	p = filepath.ToSlash(p)
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		parts := strings.Split(seg, ".")
		if len(parts) < 2 {
			continue // no dots → ordinary name (e.g. "local")
		}
		// Non-stem components only: local.md stays free; notes.local.md is withheld.
		for _, c := range parts[1:] {
			if strings.EqualFold(c, "local") {
				return true
			}
		}
	}
	return false
}

// assemble is the single seam every push bundle passes through: it applies the
// .local and .satelleignore exclusions (both listed in Bundle.Withheld), drops
// unedited embedded defaults, and sorts deterministically. Per-area walks never filter — so a new area added to
// ConfigAreas inherits the guarantee automatically. Because push, status and
// pull planning (AreaFiles) all read through here, they agree on what counts as
// substrate the operator wrote.
func assemble(files []ConfigFile, ign Ignore) Bundle {
	var kept []ConfigFile
	var withheld, defaults []string
	for _, f := range files {
		switch {
		case LocalOnlyPath(f.Path), ign.Match(f.Path):
			withheld = append(withheld, f.Path)
		case UneditedDefault(f.Path, f.Content):
			defaults = append(defaults, f.Path)
		default:
			kept = append(kept, f)
		}
	}
	sortConfigFiles(kept)
	sort.Strings(withheld)
	sort.Strings(defaults)
	return Bundle{Files: kept, Withheld: withheld, UneditedDefaults: defaults}
}

// ConfigFiles walks the ConfigAreas under repoRoot as a sync under scope would
// move them. A local scope contributes nothing. Non-local files are labeled
// PersonalTier or SharedTier for operator messaging / publish eligibility — both
// still sync to the bound project only (epic:sync-publish). A team scope is
// SharedTier wholesale; a personal scope is PersonalTier per file, promoted to
// SharedTier when FileShared (frontmatter shared:true). Reserved generated views
// (index.md/log.md/README) are excluded — they are not authored. Paths matching
// LocalOnlyPath are withheld (listed in Bundle.Withheld, never in Files). Files
// are returned sorted by Path. A non-existent area on disk is benign (nothing to
// push yet).
func ConfigFiles(cfg Config, scope Scope, repoRoot string, ign Ignore) (Bundle, error) {
	var out []ConfigFile
	for _, area := range ConfigAreas {
		files, err := filesForArea(cfg, scope, repoRoot, area)
		if err != nil {
			return Bundle{}, err
		}
		out = append(out, files...)
	}
	extra, err := teamExtraFiles(cfg, scope, repoRoot)
	if err != nil {
		return Bundle{}, err
	}
	return assemble(append(out, extra...), ign), nil
}

// TeamArea labels the team-owned files no config area holds ([team] paths such
// as scripts/*.sh or hooks/*, sty_8dedc0a4). They ride the project's config push
// as plain files, outside every area's snapshot.
const TeamArea = "team"

// teamExtraFiles is the push's share of the [team] files no config area owns:
// they move with the settings area, so under any non-local scope.
func teamExtraFiles(cfg Config, scope Scope, repoRoot string) ([]ConfigFile, error) {
	if scope == LocalScope {
		return nil, nil
	}
	return walkTeamExtraFiles(cfg, repoRoot, scope)
}

// TeamExtraFiles reads the [team] files no config area owns as a push would
// bundle them, whatever the resolved scope — what a project's first publish of
// its substrate sends whatever this machine syncs.
func TeamExtraFiles(cfg Config, repoRoot string, ign Ignore) ([]ConfigFile, error) {
	files, err := walkTeamExtraFiles(cfg, repoRoot, PersonalScope)
	if err != nil {
		return nil, err
	}
	return assemble(files, ign).Files, nil
}

// walkTeamExtraFiles walks the repo's [team] paths for the files no config area
// owns; init-owned and .local files are never team-owned (TeamSet.Owns).
func walkTeamExtraFiles(cfg Config, repoRoot string, scope Scope) ([]ConfigFile, error) {
	team := NewTeamSet(cfg.Team.Paths)
	dataDir := cfg.ResolveDataDir(repoRoot)
	seen := map[string]bool{}
	var out []ConfigFile
	for _, g := range team.Globs() {
		root := filepath.Join(dataDir, filepath.FromSlash(literalGlobPrefix(g)))
		walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == root {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, rerr := filepath.Rel(dataDir, p)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] || isReservedView(filepath.Base(rel)) || !team.Owns(rel) || SyncAreaOf(cfg, repoRoot, rel, Ignore{}) != "" {
				return nil
			}
			seen[rel] = true
			cf, ok, rerr := readConfigFile(TeamArea, p, rel, scope)
			if rerr == nil && ok {
				out = append(out, cf)
			}
			return rerr
		})
		if walkErr != nil {
			return nil, fmt.Errorf("sync [team] paths %q: %w", g, walkErr)
		}
	}
	return out, nil
}

// literalGlobPrefix is the leading segments of a glob before its first
// wildcard segment: the one dir (or file) a walk for it needs to visit.
func literalGlobPrefix(g string) string {
	var keep []string
	for _, seg := range strings.Split(g, "/") {
		if strings.ContainsAny(seg, "*?[\\") {
			break
		}
		keep = append(keep, seg)
	}
	return strings.Join(keep, "/")
}

// DocumentFiles walks the documents area under repoRoot with the same scope/
// tier labeling as ConfigFiles (local skip; SharedTier marks publish eligibility
// only). Documents is its own sync kind (order:6) — not part of ConfigAreas.
// Paths matching LocalOnlyPath are withheld.
func DocumentFiles(cfg Config, scope Scope, repoRoot string, ign Ignore) (Bundle, error) {
	files, err := filesForArea(cfg, scope, repoRoot, "documents")
	if err != nil {
		return Bundle{}, err
	}
	return assemble(files, ign), nil
}

// filesForArea walks one named area under repoRoot with scope's tier
// resolution. A local scope returns nil.
func filesForArea(cfg Config, scope Scope, repoRoot, area string) ([]ConfigFile, error) {
	if scope == LocalScope {
		return nil, nil
	}
	return walkAreaFiles(cfg, repoRoot, area, scope)
}

// AreaFiles reads one config or documents area's files exactly as a push would
// bundle them — same walk, same project-binding withholding, same .local
// withholding — but whatever the resolved scope. A pull needs the local side of
// a three-way comparison for an area whether or not this machine would push it.
func AreaFiles(cfg Config, repoRoot, area string, ign Ignore) ([]ConfigFile, error) {
	files, err := walkAreaFiles(cfg, repoRoot, area, PersonalScope)
	if err != nil {
		return nil, err
	}
	return assemble(files, ign).Files, nil
}

// AreaDefaultShas is the sha projection of UneditedDefault for one area: per
// server-relative path, the shas of the file's bytes that UneditedDefault accepts
// without a fetch — the embedded body, its current stamped form, and any on-disk
// copy of the path that UneditedDefault accepts. The sync planner holds only shas
// (and doctor runs offline), so it asks this where a snapsync.Driver.SkipBody asks
// for bytes; a stamp of an older default is not predictable here, and SkipBody
// still decides it when the bytes are fetched.
func AreaDefaultShas(cfg Config, repoRoot, area string, ign Ignore) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	add := func(path, body string) {
		if out[path] == nil {
			out[path] = map[string]bool{}
		}
		out[path][SHA(body)] = true
	}
	for rel, body := range embeddedBodyByRel() {
		if SyncAreaOf(cfg, repoRoot, rel, ign) == area {
			add(rel, body)
			add(rel, ApplyStamp(body, SHA(body)))
		}
	}
	files, err := walkAreaFiles(cfg, repoRoot, area, PersonalScope)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if !LocalOnlyPath(f.Path) && !ign.Match(f.Path) && UneditedDefault(f.Path, f.Content) {
			add(f.Path, string(f.Content))
		}
	}
	return out, nil
}

// AreaStampOnlyDefaults is the files of one area the push view (AreaFiles) drops
// as unedited embedded defaults on the strength of their own stamp alone: a
// stamped copy whose stamp matches its body but whose bytes are neither the
// current embedded body nor its current stamped form. Nothing can tell such a
// file from one edited and re-stamped, so the sync comparisons count it as a
// local file wherever the synced base or the hosted snapshot records other bytes
// for its path.
func AreaStampOnlyDefaults(cfg Config, repoRoot, area string, ign Ignore) ([]ConfigFile, error) {
	files, err := walkAreaFiles(cfg, repoRoot, area, PersonalScope)
	if err != nil {
		return nil, err
	}
	var out []ConfigFile
	for _, f := range files {
		if !LocalOnlyPath(f.Path) && !ign.Match(f.Path) && UneditedDefault(f.Path, f.Content) && stampOnlyDefault(f.Path, f.Content) {
			out = append(out, f)
		}
	}
	sortConfigFiles(out)
	return out, nil
}

// stampOnlyDefault reports whether an unedited default (UneditedDefault) is one
// only its own stamp vouches for: its bytes are neither the current embedded
// body for the path nor that body's current stamped form.
func stampOnlyDefault(rel string, body []byte) bool {
	emb, ok := embeddedBodyByRel()[rel]
	if !ok {
		return true
	}
	text := string(body)
	return text != emb && text != ApplyStamp(emb, SHA(emb))
}

// PlaceholderSettings reports whether the settings area on disk holds nothing a
// push would send: no satelle.toml, or one carrying only what push withholds — the
// repo binding `project bind` writes into a fresh clone. Such a file is a
// placeholder, not an authored copy, so a deploy must not treat it as a change
// that conflicts with the hosted settings (sty_7562055c).
func PlaceholderSettings(cfg Config, repoRoot string, ign Ignore) (bool, error) {
	files, err := AreaFiles(cfg, repoRoot, "settings", ign)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		var doc map[string]any
		if toml.Unmarshal(f.Content, &doc) != nil || !emptyTables(doc) {
			return false, nil
		}
	}
	return true, nil
}

// emptyTables reports whether a decoded TOML document has no values at all —
// only tables that are themselves empty.
func emptyTables(m map[string]any) bool {
	for _, v := range m {
		sub, ok := v.(map[string]any)
		if !ok || !emptyTables(sub) {
			return false
		}
	}
	return true
}

// SyncAreaOf names the config or documents area a server-relative path belongs
// to, or "" for a path no area owns (the synced workspace agents layer, the
// snapshot records, anything unrecognised). Server keys are stable regardless
// of [substrate_roots], so the owner is a function of the key alone — which is
// what lets a puller decide whether a hosted path the current snapshot does not
// name was published and then deleted, or was never an area's to begin with.
// A path ign matches belongs to no area either: it is never synced, so a hosted
// copy of it is not a difference. ign is passed in, never cached, so the answer
// follows the .satelleignore the command loaded.
func SyncAreaOf(cfg Config, repoRoot, path string, ign Ignore) string {
	if ign.Match(path) {
		return ""
	}
	switch path {
	case AgentsRel:
		return "agents"
	case WorkspaceAgentsRel, "":
		return ""
	case ConfigName:
		return "settings"
	case filepath.Base(cfg.ResolveConstitution(repoRoot)):
		return "constitution"
	}
	first, _, ok := strings.Cut(path, "/")
	if !ok {
		return ""
	}
	for _, area := range []string{"workflows", "principles", "skills", "tasks", "documents"} {
		if first == area {
			return area
		}
	}
	return ""
}

// walkAreaFiles is the scope-free walk behind filesForArea and AreaFiles.
func walkAreaFiles(cfg Config, repoRoot, area string, scope Scope) ([]ConfigFile, error) {
	location, isDir := ConfigAreaLocation(cfg, repoRoot, area)
	if location == "" {
		// documents (and other authored dirs) resolve via ResolveAuthoredDirs;
		// constitution/agents use dedicated paths. An area with no location is
		// not a config/document candidate on disk.
		return nil, nil
	}
	var out []ConfigFile
	if !isDir {
		serverPath := filepath.Base(location)
		if area == "agents" {
			// The agents layer lives under workflows/ (sty_10f732ed), so its server
			// key must carry that prefix: subsync deploys each key as
			// filepath.Join(dataDir, key), and a bare "agents.toml" would land every
			// pull back at the legacy path forever. A legacy-path repo pushes under
			// the canonical key too, so deploying converges it.
			serverPath = AgentsRel
		}
		if cf, ok, err := readConfigFile(area, location, serverPath, scope); err != nil {
			return nil, err
		} else if ok {
			out = append(out, cf)
		}
		return out, nil
	}
	walkErr := filepath.WalkDir(location, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == location {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(location, p)
		if rerr != nil {
			return rerr
		}
		if walkSkips(area, filepath.ToSlash(rel)) {
			return nil
		}
		serverPath := area + "/" + filepath.ToSlash(rel)
		cf, ok, rerr := readConfigFile(area, p, serverPath, scope)
		if rerr != nil {
			return rerr
		}
		if ok {
			out = append(out, cf)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("sync area %q: %w", area, walkErr)
	}
	return out, nil
}

// walkSkips reports whether a file inside a directory area is never part of that
// area's push. rel is the file's slash path relative to the area's directory.
// Skipped: a generated read-only view; in workflows, the agents layer, which sits
// IN the workflows dir but is its own sync area with its own tier (sty_10f732ed) —
// without this skip the walk would push it a second time under a workflows/ key —
// and agents.workspace.toml, the SYNCED workspace layer (sty_01949949), which came
// from the team catalog and is never pushed back as config; in tasks, everything
// but a top-level tasks/*.md definition, because tasks/<tsk>/ holds that task's
// execution runs and their outputs, which are run state, not substrate, and can be
// far larger than the server stores (sty_195dd215). The one rule behind
// walkAreaFiles and AreaOfRepoPath.
func walkSkips(area, rel string) bool {
	base := rel[strings.LastIndex(rel, "/")+1:]
	if isReservedView(base) {
		return true
	}
	switch area {
	case "workflows":
		return base == AgentsConfigName || base == WorkspaceAgentsName
	case "tasks":
		return strings.Contains(rel, "/") || !strings.HasSuffix(rel, ".md")
	}
	return false
}

// AreaOfRepoPath names the config or documents area a tree path belongs to, or ""
// for a path no area owns. path is repo-relative (as story-diff reports it) or
// absolute; the answer follows ConfigAreaLocation, so a relocated data dir or
// [substrate_roots] entry is honoured, and the skips are walkAreaFiles' own, so a
// path maps to an area exactly when that area's push would carry it. SyncAreaOf is
// the other direction of the same question for server keys, which are stable
// regardless of where the tree keeps its files.
func AreaOfRepoPath(cfg Config, repoRoot, path string) string {
	abs := filepath.FromSlash(path)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(repoRoot, abs)
	}
	abs = filepath.Clean(abs)
	// Single-file areas first: the agents layer lives under workflows/, so it
	// must be claimed before the workflows directory is.
	for _, area := range []string{"settings", "constitution", "agents"} {
		if loc, _ := ConfigAreaLocation(cfg, repoRoot, area); loc != "" && filepath.Clean(loc) == abs {
			return area
		}
	}
	if abs == filepath.Clean(filepath.Join(cfg.ResolveDataDir(repoRoot), filepath.FromSlash(AgentsRel))) {
		return "agents"
	}
	for _, area := range []string{"workflows", "principles", "skills", "tasks", "documents"} {
		loc, isDir := ConfigAreaLocation(cfg, repoRoot, area)
		if !isDir || loc == "" {
			continue
		}
		rel, err := filepath.Rel(loc, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if walkSkips(area, filepath.ToSlash(rel)) {
			continue
		}
		return area
	}
	return ""
}

// readConfigFile reads one file's bytes and resolves its tier. serverPath is the
// already-computed server-relative key; absPath is the on-disk source. A missing
// single-file area (constitution/agents/settings not yet seeded) is benign — ok=false.
// Content is the file verbatim, except that stripRepoBinding withholds a
// satelle.toml's project binding (reported in ConfigFile.Withheld).
func readConfigFile(area, absPath, serverPath string, scope Scope) (ConfigFile, bool, error) {
	body, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ConfigFile{}, false, nil
		}
		return ConfigFile{}, false, fmt.Errorf("read %s: %w", absPath, err)
	}
	body, withheld := stripRepoBinding(area, body)
	tier := PersonalTier
	if scope == TeamScope {
		tier = SharedTier
	} else if scope == PersonalScope && FileShared(PersonalScope, string(body)) {
		tier = SharedTier
	}
	return ConfigFile{Area: area, Path: serverPath, Tier: tier, Content: body, Withheld: withheld}, true, nil
}

// stripRepoBinding withholds the keys that bind a checkout to a hosted project
// from a file about to leave the machine, and names what it withheld. For the
// settings area (satelle.toml) it removes [sync] project and [hosted] project so a
// push cannot rebind another repo, and every RemovedSyncKeys key so a push
// never carries a scope or a removed key; deploy re-applies the local binding after restore (the
// load-bearing guard against older hosted copies that carry one). Every other
// area — the agents layer included — passes through byte-identical:
// machine-specific agents content is refused at push (CheckAgents), never altered.
func stripRepoBinding(area string, body []byte) ([]byte, []string) {
	if area != "settings" {
		return body, nil
	}
	s := string(body)
	var withheld []string
	for _, k := range []struct{ section, label string }{{"sync", "[sync] project"}, {"hosted", "[hosted] project"}} {
		if next := RemoveKey(s, k.section, "project"); next != s {
			withheld = append(withheld, k.label)
			s = next
		}
	}
	next, removed := RemoveSyncScopeKeys(s)
	for _, k := range removed {
		withheld = append(withheld, "[sync] "+k)
	}
	return []byte(next), withheld
}

// sortConfigFiles orders files by server Path for a deterministic push.
func sortConfigFiles(files []ConfigFile) {
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j-1].Path > files[j].Path; j-- {
			files[j-1], files[j] = files[j], files[j-1]
		}
	}
}

// isReservedView reports whether a file basename is a generated read-only view
// (index.md, log.md) or a non-substrate README — never authored, so never part
// of a config push. Shared with the `sync scopes` shared-file scan.
func isReservedView(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base == "index" || base == "log" || strings.EqualFold(base, "README")
}

// ConfigAreaLocation resolves a config or documents area's on-disk location.
// isDir reports whether location is a directory to walk (vs. a single file to
// read directly). It mirrors the cli syncAreaPath resolution but lives in
// config so the walk is unit-testable without the local store. documents is
// resolved here too so DocumentFiles can reuse the same path helper.
func ConfigAreaLocation(cfg Config, repoRoot, area string) (location string, isDir bool) {
	dataDir := cfg.ResolveDataDir(repoRoot)
	switch area {
	case "constitution":
		return cfg.ResolveConstitution(repoRoot), false
	case "agents":
		path, _ := AgentsPath(dataDir)
		return path, false
	case "tasks":
		return filepath.Join(dataDir, "tasks"), true
	case "settings":
		// Canonical path is dataDir/satelle.toml — where init and project bind
		// write. An operator who relocates config via SATELLE_CONFIG is not
		// covered at push (readConfigFile returns ok=false for a missing file).
		return filepath.Join(dataDir, ConfigName), false
	default:
		// AuthoredKinds (documents/workflows/principles/skills) + any other
		// directory-shaped area under dataDir.
		if dir := cfg.ResolveAuthoredDirs(repoRoot)[area]; dir != "" {
			return dir, true
		}
		return "", false
	}
}

// fmBoolScalar extracts a top-level YAML boolean scalar named key from a
// `---`-fenced frontmatter block (or a full markdown body carrying one).
// Missing, malformed, or non-"true" values fail closed to false.
func fmBoolScalar(body, key string) bool {
	lines := strings.Split(body, "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		start = 1
	}
	prefix := key + ":"
	for i := start; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" {
			break
		}
		if t == prefix || strings.HasPrefix(t, prefix+" ") {
			v := strings.TrimSpace(strings.TrimPrefix(t, prefix))
			v = strings.Trim(v, `"'`)
			return v == "true"
		}
	}
	return false
}
