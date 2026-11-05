// Package config is satelle's trimmed TOML configuration, ported from
// satellites' internal/cliconfig with the server/token/credstore surface
// dropped (the OSS tier is always local — no remote dispatch). What remains:
//
//   - Repo-root resolution: walk up from CWD for .satelle/satelle.toml.
//   - Defaults for every setting — a repo with no satelle.toml runs zero-config.
//   - A gitignored satelle.local.toml overlay beside the committed config.
//   - [substrate_roots]: per-kind authored-markdown dirs, which MAY live outside
//     .satelle/ (the directory monitor watches whatever these point at).
//   - data_dir / db: where the per-repo sqlite database lives.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// Defaults applied when a key is unset. Every setting has one, so a repo with
// an empty (or absent) satelle.toml runs on defaults for all keys.
const (
	// DefaultDataDir is the per-repo home for satelle's data stores —
	// <repo>/.satelle. A relative data_dir resolves under the repo root.
	DefaultDataDir = ".satelle"
	// DefaultDBName is the sqlite database file inside data_dir.
	DefaultDBName = "satelle.db"
	// DefaultConstitutionName is the project constitution file at the data-dir
	// root (.satelle/constitution.md) — the order-zero doc injected every session
	// (epic:session-context). It lives OUTSIDE the authored-kind dirs, so it is
	// read directly, not indexed as a kind.
	DefaultConstitutionName = "constitution.md"
	// DefaultWebPort is the local web server's listen port.
	DefaultWebPort = 8787
	// DefaultLogLevel is arbor's level when log_level is unset.
	DefaultLogLevel = "info"
	// DefaultLogsMaxSizeKB caps each flat evidence log under the home-keyed
	// runtime logs directory (~/.satelle/<repo-key>/logs; .satelle/logs is a symlink pointer to it) before it rolls; DefaultLogsMaxFiles bounds how
	// many rotations are kept. Daily rolling is always on regardless of these
	// (sty_a67e6e8c).
	DefaultLogsMaxSizeKB = 5120 // 5 MiB
	DefaultLogsMaxFiles  = 7
	// ConfigName / LocalConfigName are the committed config and its gitignored
	// per-user overlay, both under <repo>/.satelle/.
	ConfigName      = "satelle.toml"
	LocalConfigName = "satelle.local.toml"
)

// AuthoredKinds are the markdown-source-of-truth artifact kinds the directory
// monitor watches and indexes. Each defaults to <repo>/.satelle/<kind> and is
// individually relocatable via [substrate_roots] (may be outside .satelle/).
var AuthoredKinds = []string{"documents", "workflows", "principles", "skills"}

// Config is the on-disk shape at .satelle/satelle.toml. Every field is
// optional; the Resolve* methods supply defaults so the zero value is valid.
type Config struct {
	// DataDir is the per-repo store home; empty means DefaultDataDir. A
	// relative value resolves under the repo root, never the process CWD.
	DataDir string `toml:"data_dir"`
	// DB overrides the database path; empty means <data_dir>/satelle.db. A
	// relative value resolves under the repo root.
	DB string `toml:"db"`
	// SubstrateRoots maps an authored kind to the parent dir holding it. UNSET
	// for a kind means the default <data_dir>/<kind>. An absolute value lets a
	// kind's source live anywhere on disk.
	SubstrateRoots map[string]string `toml:"substrate_roots"`
	// LogLevel is arbor's level (debug|info|warn|error); empty means info.
	LogLevel string `toml:"log_level"`
	// LogsMaxSizeKB caps each flat evidence log under the home-keyed runtime logs
	// directory (~/.satelle/<repo-key>/logs; .satelle/logs is a symlink pointer)
	// before it rolls;
	// zero means DefaultLogsMaxSizeKB. LogsMaxFiles bounds kept rotations; zero means
	// DefaultLogsMaxFiles. Daily rolling is always on.
	LogsMaxSizeKB int `toml:"logs_max_size_kb"`
	LogsMaxFiles  int `toml:"logs_max_files"`
	// StoriesKeepClosed keeps the N most-recently-updated CLOSED-story attachment
	// dirs under .satelle/stories; 0 (default) disables count-based pruning.
	// StoriesKeepDays prunes a closed story's dir when the story's terminal update
	// is older than N days; 0 (default) disables age-based pruning. The two
	// compose — either triggers pruning. A NON-terminal story's dir is ALWAYS kept
	// regardless of either setting. Pruning MOVES the dir to
	// .satelle/backups/stories/ (never deletes in place). (sty_aba7200c)
	StoriesKeepClosed int `toml:"stories_keep_closed"`
	StoriesKeepDays   int `toml:"stories_keep_days"`
	// RetrieveKeepDays prunes a CCR retrieval ref (and its blob, once no ref
	// remains) once the owning story is terminal and its terminal update is
	// older than N days; 0 (default) keeps every original forever, so gate
	// evidence audited later is never pulled out from under a reviewer
	// (sty_b0577532). A non-terminal story's ref is always kept regardless.
	RetrieveKeepDays int `toml:"retrieve_keep_days"`
	// Review opts this repo into reviewer-gated create. satelle init seeds
	// gate_create = true (misclassification is cheapest to catch at create —
	// sty_83782ffb); set false to opt out. Absent key stays false so older
	// configs without [review] do not silently change until re-init/edit.
	Review ReviewConfig `toml:"review"`
	// Gate tunes the PreToolUse edit gate (the `satelle hook gate` handler).
	Gate GateConfig `toml:"gate"`
	// Engagement selects this project's SEAT CONCURRENCY MODE (sty_c098dc2d).
	// Absent = "none", today's single-occupancy seat. See EngagementConfig.
	Engagement EngagementConfig `toml:"engagement"`
	// Hosted records the hosted-server binding for `satelle login` — the server
	// URL and the project slug. Both are committed (secret-free); the OAuth
	// access/refresh TOKENS are NEVER stored here — they live in the user-level
	// credential store outside the repo (see internal/hosted). (sty_2fc93374)
	Hosted HostedConfig `toml:"hosted"`
	// Backup tunes pre-mutation substrate backups (sty_873a5380). Local copies
	// under .satelle/backups/ are always written; LocalOnly suppresses the
	// advisory about enabling online/personal backup when no hosted channel is
	// configured.
	Backup BackupConfig `toml:"backup"`
	// Vars is the operator KV an agents.toml binding's env values substitute via
	// ${NAME} (sty_001558ce) — how a dispatched step points at an alternate model
	// backend (e.g. GLM's Anthropic-compatible endpoint) without a wrapper binary.
	// NON-secret vars MAY live in the committed satelle.toml; SECRETS (API keys)
	// go in the gitignored satelle.local.toml overlay, whose keys win per-key. The
	// KV is file-only — never persisted to the DB and never included in a substrate
	// push (satelle.local.toml is excluded), so secrets do not leave the machine.
	Vars map[string]string `toml:"vars"`
	// Sync maps a .satelle area name (see SyncAreas) to its scope on the
	// local|personal|shared ladder (epic:scoped-sync). Unset means "local" —
	// nothing syncs without explicit opt-in. A committed [sync] table sets team
	// defaults; satelle.local.toml overrides per-area for a single developer, the
	// same per-key overlay merge as Vars (sty_a2d2e057).
	Sync SyncTable `toml:"sync"`
	// Team declares the team-owned paths a bound repo resolves from its project
	// tier (see TeamConfig).
	Team TeamConfig `toml:"team"`
	// Tags holds the optional controlled-vocabulary table for work-item tags
	// (sty_034d843c / epic:surface-scoped-steps). Declared namespaces reject
	// unknown values at story/task create and set; absent/empty means every tag
	// is free-form. The vocabulary is REPO CONFIG — the binary never hardcodes
	// which namespaces or values exist.
	Tags TagsConfig `toml:"tags"`
	// Categories holds the optional controlled category vocabulary (sty_b2315e17).
	// Default list ships embedded (substrate/config/categories.toml); satelle.toml
	// may extend (extra) or replace (vocabulary). Enforce is off|warn|reject
	// (default warn). Values are never a Go literal.
	Categories CategoriesConfig `toml:"categories"`
	// Harness holds per-harness [harness.<name>] tables — the SessionStart
	// injection limit (sty_ce1a2733) and the session-context event plus its
	// character clip. Embedded defaults live in substrate/config/harness.toml;
	// a key here overrides them per harness.
	Harness map[string]HarnessConfig `toml:"harness"`
	// FixLane is the bound on the in-loop fix lane (sty_4b694872): the size
	// ceiling and the path classes a claim may never touch. Embedded default in
	// substrate/config/fix_lane.toml; see ResolveFixLane.
	FixLane FixLaneConfig `toml:"fix_lane"`
	// Attachments bounds binary story attachments (sty_40e5a305): max decoded
	// size and the content-type allowlist. Defaults ship in code as mechanism
	// bounds; a repo may override either in satelle.toml.
	Attachments AttachmentsConfig `toml:"attachments"`
	// Dispatch tunes per-dispatch mechanism that is not gate policy: today,
	// only the leftover-file sweep (sty_e7aaf8b1). Kept separate from
	// [gate] because it runs at session close, not the PreToolUse hook.
	Dispatch DispatchConfig `toml:"dispatch"`
	// InstructionReview names the extra instruction paths the shipped trigger
	// skill counts (sty_012394c0). See InstructionReviewConfig.
	InstructionReview InstructionReviewConfig `toml:"instruction_review"`
	// Validate tunes what `satelle validate` reports beyond structure — today the
	// injected-context size and its warn-only budgets. See ValidateConfig.
	Validate ValidateConfig `toml:"validate"`
	// Doctor tunes the context audit `satelle doctor` runs (the verbatim
	// duplication threshold). The default ships embedded in
	// substrate/config/doctor.toml; see ResolveDuplicateMinWords.
	Doctor DoctorConfig `toml:"doctor"`
	// Worktree declares what a worktree opened for a story needs
	// (sty_804c566b): the gitignored paths it carries from the main tree and the
	// branch and location templates. See WorktreeConfig.
	Worktree WorktreeConfig `toml:"worktree"`
	// Trunk declares the start-of-work trunk check (sty_9f3e51d1): whether it
	// runs at engage, and which states it refuses on. See TrunkConfig.
	Trunk TrunkConfig `toml:"trunk"`
	// Release declares the metadata a recorded release may carry beyond its
	// commit and release identifier (`satelle story release-record`). See
	// ReleaseConfig.
	Release ReleaseConfig `toml:"release"`
	// Forge selects the forge provider PR reads, reviews and commit statuses go
	// through, and names the approval's status context. See ForgeConfig.
	Forge ForgeConfig `toml:"forge"`
}

// ReleaseConfig is the [release] table. Fields lists the extra metadata keys a
// release record may carry (--field k=v); a key outside it is refused. Empty =
// any key is accepted. The binary names no field of its own.
type ReleaseConfig struct {
	Fields []string `toml:"fields"`
}

// IsAgentCaller detects a dispatched or in-loop agent process by environment:
// SATELLE_SCRATCH (every dispatched agent gets one — sty_e7aaf8b1),
// SATELLE_SESSION (satelle's own harness-neutral session marker), or an
// in-loop harness's own marker as recognised by the agentcli adapters.
// Mechanism only — whether to default compact mode ON for one is
// OutputConfig.CompactForAgents, authored configuration.
func IsAgentCaller() bool {
	if strings.TrimSpace(os.Getenv(ScratchEnv)) != "" || strings.TrimSpace(os.Getenv(SessionEnv)) != "" {
		return true
	}
	_, ok := agentcli.InLoopHarnessFromEnv(os.Environ())
	return ok
}

// DispatchConfig tunes per-dispatch/session mechanism: today, only the
// leftover-file sweep a coder session runs at close (sty_e7aaf8b1).
type DispatchConfig struct {
	Leftovers LeftoverRule `toml:"leftovers"`
}

// Leftover actions — the closed set LeftoverRule.Action accepts. Empty
// resolves to LeftoverActionMove (ResolveAction).
const (
	LeftoverActionMove = "move"
	LeftoverActionFlag = "flag"
)

// LeftoverRule is language-neutral configuration for what counts as debris a
// coder session left in the working tree (sty_e7aaf8b1, satelle-story-
// architecture-review revision 2: the binary ships no patterns, no regex, and
// no opinion about any language — e.g. Go test files — of its own). A file is
// a leftover when it is untracked, was created during the session being
// swept (a before/after snapshot diff — never a pre-existing untracked file),
// and matches a glob in Patterns (against its repo-relative path or its
// basename) OR its content matches ContentRegex within MaxBytes. Empty
// Patterns and empty ContentRegex mean "sweep nothing" — the default, since
// the binary ships no opinion.
type LeftoverRule struct {
	// Patterns are globs (filepath.Match) matched against the repo-relative
	// path and against the basename.
	Patterns []string `toml:"patterns"`
	// ContentRegex, when set, flags a file whose FULL content matches it.
	ContentRegex string `toml:"content_regex"`
	// MaxBytes bounds which files are read for the ContentRegex check; 0
	// means unbounded. Patterns matching never consult MaxBytes.
	MaxBytes int `toml:"max_bytes"`
	// Action is "move" (default) or "flag". Move relocates the file under the
	// dispatch's scratch directory (<scratch>/leftovers/<relpath>); flag
	// leaves it in place and only records it on the ledger.
	Action string `toml:"action"`
}

// ResolveAction returns the effective leftover action: the configured value,
// or LeftoverActionMove when unset.
func (r LeftoverRule) ResolveAction() string {
	if r.Action == LeftoverActionFlag {
		return LeftoverActionFlag
	}
	return LeftoverActionMove
}

// AttachmentsConfig is the [attachments] table: size cap and content-type
// allowlist for binary story attachments (sty_40e5a305). Zero/empty fields
// resolve to DefaultAttachmentMaxBytes / DefaultAttachmentAllowTypes.
type AttachmentsConfig struct {
	// MaxBytes is the per-attachment decoded size cap. Zero means the default.
	MaxBytes int64 `toml:"max_bytes"`
	// AllowTypes is the content-type allowlist. Empty means the default set.
	// image/svg+xml and text/html are never defaults — both execute if served.
	AllowTypes []string `toml:"allow_types"`
}

// DefaultAttachmentMaxBytes is 10 MiB — covers screenshots and ordinary PDFs
// while bounding the runtime plane and any future sync payload (sty_40e5a305).
const DefaultAttachmentMaxBytes int64 = 10 << 20

// DefaultAttachmentAllowTypes is the conservative binary content-type allowlist
// (sty_40e5a305). SVG and HTML are deliberately absent: both execute script in a
// browser if a delivery route is ever added.
var DefaultAttachmentAllowTypes = []string{
	"image/png",
	"image/jpeg",
	"image/gif",
	"image/webp",
	"application/pdf",
	"application/zip",
	"application/octet-stream",
}

// ResolveAttachmentMaxBytes returns the per-attachment size cap.
func (c Config) ResolveAttachmentMaxBytes() int64 {
	if c.Attachments.MaxBytes > 0 {
		return c.Attachments.MaxBytes
	}
	return DefaultAttachmentMaxBytes
}

// ResolveAttachmentAllowTypes returns the effective content-type allowlist.
func (c Config) ResolveAttachmentAllowTypes() []string {
	if len(c.Attachments.AllowTypes) > 0 {
		out := make([]string, 0, len(c.Attachments.AllowTypes))
		for _, t := range c.Attachments.AllowTypes {
			if s := strings.TrimSpace(strings.ToLower(t)); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return append([]string(nil), DefaultAttachmentAllowTypes...)
}

// BackupConfig is the operator policy for pre-mutation substrate backups
// (sty_873a5380, sty_84f14ace). Local floor is always on; this tunes the
// advisory and the opt-in hosted documents push.
type BackupConfig struct {
	// LocalOnly means keep backups under .satelle/backups/ only and do not emit
	// the "enable online backup" advisory. Prefer satelle.local.toml for a
	// per-developer choice.
	LocalOnly bool `toml:"local_only"`
	// Hosted opts into pushing pre-images to the bound project's personal
	// documents partition (path backups/<rel>). Default false: that partition
	// is also listed by documents pull, and backups/ is a restore exclusion —
	// auto-push used to permanently wedge sync (sty_84f14ace). Operators who
	// set hosted = true re-introduce poison into the partition but, post-
	// unwedge, pull only skips those paths rather than failing the whole sync.
	Hosted bool `toml:"hosted"`
}

// HostedConfig binds a repo to a hosted satelle-server. Secret-free and
// committed in satelle.toml; the login tokens are stored per-user outside the
// repo (internal/hosted credential store), never here.
type HostedConfig struct {
	// Server is the hosted-server base URL (e.g. https://hosted.satelle.dev).
	Server string `toml:"server"`
	// Project is the hosted project slug this repo maps to. Personal sync
	// (config/documents/workstate) targets this project's collection on the
	// server — not a shared dump across every project under the personal
	// workspace (sty_0aa3df89). Written by `satelle project bind <slug>`.
	Project string `toml:"project"`
	// Workspace is the ACTIVE hosted workspace scoped sync routes personal+shared
	// areas through (epic:scoped-sync) — the handle the future sync engine
	// prefixes. Unset (or the personal sentinel) means the developer's OWN
	// workspace, the zero-config default; any other value is a team-workspace
	// NAME the developer elected. This is PER-DEVELOPER: `satelle login
	// --workspace <name>` records the choice in the gitignored satelle.local.toml
	// overlay, and a value committed HERE is a hand-authored TEAM DEFAULT the
	// overlay overrides per-key. Resolve via ResolveActiveWorkspace.
	Workspace string `toml:"workspace"`
}

// ReviewConfig toggles the quality-management gates for a repo.
type ReviewConfig struct {
	// GateCreate runs the required-structure reviewer on story/task creation,
	// pushing non-conforming drafts back instead of persisting them.
	GateCreate bool `toml:"gate_create"`
}

// GateConfig tunes the PreToolUse edit gate and the single-story process rule.
// EditExemptPaths lists repo-relative (or absolute) path prefixes whose edits are
// exempt from the engaged-story gate. It is the SOLE exemption source: the binary
// does NOT special-case the data dir or any managed path (configuration over code,
// the constitution). `satelle init` seeds ".satelle/" and ".gitignore" here so
// authored substrate and satelle-managed .gitignore output (init/migrate write
// its managed block) stay editable without a release OOTB, but the operator owns
// the list — add a harness authoring dir (e.g. ".claude/", which holds authored
// skills, not product code) or drop either default to gate those paths too.
// Empty means everything in-repo requires an engaged story (sty_8c3d345c /
// sty_f115e6bf).
//
// EditExemptGlobs lists filename globs whose edits are also exempt (sty_fefc88cd).
// Basename match unless the pattern contains `/` (then repo-relative). Sibling
// of EditExemptPaths — prefix semantics stay exact. Init seeds story-reference
// dump names; the operator owns the list.
//
// How MANY stories may perform at once is NOT a gate concern: it is the seat
// concurrency mode, [engagement] parallel (sty_c098dc2d), whose default "none"
// is the single performing story this repo has always enforced (sty_c7149f8a).
// The removed allow_parallel bool (sty_a614a0ea) is not coming back — the mode
// is a closed named set, see EngagementConfig.
//
// AllowOutsideTreeEdits opts INTO Bash/Edit mutations whose targets resolve
// inside another git working tree (root differs from the session-home anchor;
// sty_a8454d10 / sty_aadd4d6c). Non-repo paths (temp, scratchpads) are never
// fenced. Default false = deny foreign-tree mutations (create stories in other
// repos stays allowed; progressing/mutating them from this session does not).
// true only for a deliberately multi-repo install — it relaxes containment
// only; the engaged-story rule is untouched.
//
// CommandAllow is an OPT-IN step-scoped command policy (sty_c21490cc). Keys are
// git subcommands (e.g. "push", "commit"); values are story statuses that may
// run them while engaged. Empty/nil = no step restriction (today's engage-only
// commitgate). Example:
//
//	[gate.command_allow]
//	push = ["release"]
//
// Not a satelle default — the operator authors the policy per repo.
//
// NoImplementModels / NoImplementMessage are the model-role edit rule
// (sty_bdeff052). Absent list ⇒ the hook makes no model decision. The binary
// ships neither a default glob nor a default message. NoImplementExemptPaths
// and NoImplementExemptGlobs are THAT rule's carve-out (empty ⇒ the rule
// applies everywhere it is configured). They are not edit_exempt_paths —
// that list stays the sole engaged-story exemption.
type GateConfig struct {
	// EditExemptPaths are repo-root-relative (or absolute) prefixes whose
	// edits skip the engaged-story gate. An absolute prefix that sits
	// outside the session repo (e.g. /tmp/) never exempts a path inside
	// that repo — it only covers out-of-tree drafts.
	EditExemptPaths []string `toml:"edit_exempt_paths"`
	EditExemptGlobs []string `toml:"edit_exempt_globs"`
	// [gate] lock_substrate_paths is deliberately NOT a field: an absent key
	// (default lock) and `= []` (opt-out) decode alike here, so the lock reads
	// the raw file through ParseLockSubstratePaths.
	AllowOutsideTreeEdits  bool                `toml:"allow_outside_tree_edits"`
	CommandAllow           map[string][]string `toml:"command_allow"`
	CommandRules           []CommandRule       `toml:"command_rule"`
	CommandPolicies        []CommandPolicy     `toml:"command_policy"`
	NoImplementModels      []string            `toml:"no_implement_models"`
	NoImplementMessage     string              `toml:"no_implement_message"`
	NoImplementExemptPaths []string            `toml:"no_implement_exempt_paths"`
	NoImplementExemptGlobs []string            `toml:"no_implement_exempt_globs"`
	// Handoff chooses whether a gate-running verb called by an agent (or with no
	// terminal) returns a handle and delivers its verdict later, instead of
	// holding the foreground until the reviewer finishes. One of GateHandoffAuto
	// (the default), GateHandoffOn, GateHandoffOff. The SATELLE_GATE_MODE
	// environment variable overrides it. A value outside the set is refused at
	// load (ValidateGateHandoff).
	Handoff string `toml:"handoff"`
}

// Gate handoff choices for [gate] handoff (sty_8ee31f26). The set is CLOSED.
const (
	// GateHandoffAuto hands off only when a configured driver harness has a
	// background cutoff under the fastest measured gate time (the mechanism
	// decides; see agentcli.HandoffNeeded and GateForegroundBudget). It is not a
	// guarantee for a harness above that line whose edges run longer. It is the default.
	GateHandoffAuto = "auto"
	// GateHandoffOn hands off for every agent caller or caller with no terminal.
	GateHandoffOn = "on"
	// GateHandoffOff always runs the gate in the foreground.
	GateHandoffOff = "off"
)

// ResolveGateHandoff returns the configured handoff choice, defaulting an
// absent/blank value to GateHandoffAuto. Load has already refused anything
// outside the closed set.
func (c Config) ResolveGateHandoff() string {
	if s := strings.TrimSpace(c.Gate.Handoff); s != "" {
		return s
	}
	return GateHandoffAuto
}

// ValidateGateHandoff refuses a [gate] handoff value outside the closed set,
// naming the supported values. Matching is exact. Blank/absent is valid (it
// means the default). path is named so a multi-file overlay says which file to
// fix.
func ValidateGateHandoff(c Config, path string) error {
	switch v := strings.TrimSpace(c.Gate.Handoff); v {
	case "", GateHandoffAuto, GateHandoffOn, GateHandoffOff:
		return nil
	default:
		return fmt.Errorf(
			"config: %s: [gate] handoff = %q is not a handoff choice — supported values are %q (hand off only where a driver harness has a background cutoff), %q and %q",
			path, v, GateHandoffAuto, GateHandoffOn, GateHandoffOff)
	}
}

// Command rule actions for [[gate.command_rule]] (sty_a74551d6). The set is
// CLOSED. satelle ships no rule: the repo authors every one.
const (
	// CommandRuleDeny refuses a matching command before it runs.
	CommandRuleDeny = "deny"
	// CommandRuleWarn lets a matching command run and tells the agent why it was
	// flagged, where the harness can deliver that.
	CommandRuleWarn = "warn"
)

// CommandRule is one repo-authored judgement of an agent's shell command line.
// Pattern is a whitespace-split argv prefix; see CommandRuleToken for the token
// forms (a word, *, **, **/NAME as the first token, and *TEXT*). Allow lists the story statuses or workflow step names at which the
// command passes; empty means no status or step lets it pass. Message is shown
// to the agent. The first matching rule, in file order, wins.
type CommandRule struct {
	Pattern string   `toml:"pattern"`
	Action  string   `toml:"action"`
	Allow   []string `toml:"allow"`
	Message string   `toml:"message"`
	// Tokens is Pattern classified once by ValidateGateCommandRules, so the hook
	// does not parse it on every command. Never read from TOML.
	Tokens []CommandRuleToken `toml:"-"`
	// Source names the subscription a rule came from; empty for a rule the repo
	// authored. Never read from TOML.
	Source string `toml:"-"`
}

// Compiled is the rule's classified pattern: the one load validated, or a parse
// into buf for a rule built without validation.
func (r CommandRule) Compiled(buf []CommandRuleToken) ([]CommandRuleToken, error) {
	if r.Tokens != nil {
		return r.Tokens, nil
	}
	return AppendCommandRuleTokens(buf[:0], r.Pattern)
}

// CommandRuleError is a [[gate.command_rule]] that load refused. It names the
// file, the rule index and the field, and never the value, so a hook can show
// it to an agent without echoing a rule's text. Index is -1 when the problem is
// a key no field decodes.
type CommandRuleError struct {
	Path   string
	Index  int
	Field  string
	Reason string
}

func (e *CommandRuleError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("config: %s: [[gate.command_rule]] key %s: %s", e.Path, e.Field, e.Reason)
	}
	return fmt.Sprintf("config: %s: [[gate.command_rule]] rule %d field %s: %s", e.Path, e.Index, e.Field, e.Reason)
}

// ParseError is a TOML syntax error in a config file. A hook that cannot tell
// whether the file declared command rules fails closed on it.
type ParseError struct {
	Path string
	Err  error
}

func (e *ParseError) Error() string { return fmt.Sprintf("config: parse %s: %v", e.Path, e.Err) }
func (e *ParseError) Unwrap() error { return e.Err }

// CommandRuleKind is how one pattern token compares with argv.
type CommandRuleKind int

const (
	// CommandRuleWord matches a token equal to Text.
	CommandRuleWord CommandRuleKind = iota
	// CommandRuleAny is "*": any one token.
	CommandRuleAny
	// CommandRuleRun is "**": any number of tokens, none included.
	CommandRuleRun
	// CommandRuleProgram is "**/NAME", first token only: argv[0] whose text
	// after its last "/" is Text, however the program was invoked.
	CommandRuleProgram
	// CommandRuleContains is "*TEXT*": one token holding Text.
	CommandRuleContains
)

// CommandRuleToken is one classified token of a rule pattern.
type CommandRuleToken struct {
	Kind CommandRuleKind
	Text string
}

// ParseCommandRulePattern classifies a rule pattern's whitespace-split tokens.
// It is the one grammar: validation and matching both read it. The error is a
// reason only, never the pattern text.
func ParseCommandRulePattern(pattern string) ([]CommandRuleToken, error) {
	return AppendCommandRuleTokens(nil, pattern)
}

var (
	errRuleSyntax   = errors.New("tokens must be plain words or wildcards, with no shell syntax or quotes")
	errRuleProgPos  = errors.New("**/NAME is allowed only as the first token")
	errRuleProgName = errors.New("**/NAME needs a program name with no / or *")
	errRuleWildcard = errors.New("a token holding * must be exactly *, **, **/NAME or *TEXT*")
)

// AppendCommandRuleTokens is ParseCommandRulePattern into dst, so a caller with
// a stack buffer classifies a pattern without allocating.
func AppendCommandRuleTokens(dst []CommandRuleToken, pattern string) ([]CommandRuleToken, error) {
	start := -1
	for i := 0; i <= len(pattern); i++ {
		if i < len(pattern) && pattern[i] != ' ' && (pattern[i] < '\t' || pattern[i] > '\r') {
			if start < 0 {
				start = i
			}
			continue
		}
		if start < 0 {
			continue
		}
		f := pattern[start:i]
		start = -1
		for j := 0; j < len(f); j++ {
			if commandRuleMeta[f[j]] {
				return nil, errRuleSyntax
			}
		}
		switch {
		case f == "*":
			dst = append(dst, CommandRuleToken{Kind: CommandRuleAny})
		case f == "**":
			dst = append(dst, CommandRuleToken{Kind: CommandRuleRun})
		case strings.HasPrefix(f, "**/"):
			name := f[len("**/"):]
			if len(dst) != 0 {
				return nil, errRuleProgPos
			}
			if name == "" || strings.ContainsAny(name, "/*") {
				return nil, errRuleProgName
			}
			dst = append(dst, CommandRuleToken{Kind: CommandRuleProgram, Text: name})
		case !strings.Contains(f, "*"):
			dst = append(dst, CommandRuleToken{Kind: CommandRuleWord, Text: f})
		case len(f) > 2 && f[0] == '*' && f[len(f)-1] == '*' && !strings.Contains(f[1:len(f)-1], "*"):
			dst = append(dst, CommandRuleToken{Kind: CommandRuleContains, Text: f[1 : len(f)-1]})
		default:
			return nil, errRuleWildcard
		}
	}
	return dst, nil
}

// commandRuleMetaChars are shell syntax a pattern token may not hold: a rule
// matches the argv tokens of one simple command, never shell syntax.
const commandRuleMetaChars = ";&|<>()$`\\\"'"

// commandRuleMeta is commandRuleMetaChars as a byte table, so classifying a
// token on the hook path is one pass with no set built per call.
var commandRuleMeta = func() (m [256]bool) {
	for i := 0; i < len(commandRuleMetaChars); i++ {
		m[commandRuleMetaChars[i]] = true
	}
	return m
}()

// ValidateGateCommandRules refuses a malformed [[gate.command_rule]] with a
// *CommandRuleError. It checks closed sets only and holds no workflow
// knowledge, so a stale allow name is doctor's finding, not a hook failure.
// path names the file that declared the rules.
func ValidateGateCommandRules(c Config, path string) error {
	bad := func(i int, field, reason string) error {
		return &CommandRuleError{Path: path, Index: i, Field: field, Reason: reason}
	}
	for i, r := range c.Gate.CommandRules {
		toks, err := ParseCommandRulePattern(r.Pattern)
		if err != nil {
			return bad(i, "pattern", err.Error())
		}
		if len(toks) == 0 {
			return bad(i, "pattern", "must not be empty")
		}
		c.Gate.CommandRules[i].Tokens = toks
		switch r.Action {
		case CommandRuleDeny, CommandRuleWarn:
		default:
			return bad(i, "action", fmt.Sprintf("must be %q or %q", CommandRuleDeny, CommandRuleWarn))
		}
		if strings.TrimSpace(r.Message) == "" {
			return bad(i, "message", "must not be empty")
		}
		for _, a := range r.Allow {
			if strings.TrimSpace(a) == "" {
				return bad(i, "allow", "entries must not be empty")
			}
		}
	}
	return nil
}

// undecodedCommandRuleKeys refuses a key under [[gate.command_rule]] that no
// field decodes, so a misspelled "alow" cannot silently turn a rule into a
// deny-everywhere one.
func undecodedCommandRuleKeys(md toml.MetaData, path string) error {
	for _, k := range md.Undecoded() {
		if len(k) >= 3 && k[0] == "gate" && k[1] == "command_rule" {
			return &CommandRuleError{Path: path, Index: -1, Field: strings.Join(k[2:], "."), Reason: "unknown key"}
		}
	}
	return nil
}

// Seat concurrency modes for [engagement] parallel (sty_c098dc2d). The set is
// CLOSED — Load refuses anything else, naming these three.
const (
	// ParallelNone is the default: one performing story per project. Every
	// engagement is keyed uniquely by its own id, so any other live seat
	// holder conflicts.
	ParallelNone = "none"
	// ParallelEpic keys the seat on the story's PARENT id, so sibling children
	// of one epic may hold sub-leases concurrently — each from a distinct git
	// working tree. A story under a different parent (or none) is refused while
	// an epic-keyed seat is held. The parent is not engaged unless its route
	// declares a step with after_children: the epic id is an arbitration KEY, and
	// a container waiting on its children holds no lease.
	ParallelEpic = "epic"
	// ParallelWorktree lets any number of stories hold seats at once — from
	// different epics, or none — each from its OWN git working tree (sty_69ec7fbc).
	// Tree identity is the arbitration: a second engagement from a tree that
	// already holds a seat is refused, naming the holder. Epic membership still
	// matters in one place only: the children of a sequential epic admit one at
	// a time, exactly as under epic.
	ParallelWorktree = "worktree"
)

// EngagementConfig selects the per-project seat concurrency mode.
//
//	[engagement]
//	parallel = "none"   # default — one performing story at a time
//	parallel = "epic"   # siblings of one epic, one working tree per lease
//
// Satelle arbitrates concurrent ENGAGEMENT only. How siblings converge (merge
// order, batch-head suite runs, one push) is the repo's own substrate — the
// binary performs no sibling-contention policing (sty_c098dc2d).
//
// A value outside the closed set is refused at load (Load → ValidateEngagement).
// Note bare toml decoding ignores unknown keys, so a MISSPELLED section name
// silently yields the default "none" — failing closed to today's behaviour.
type EngagementConfig struct {
	Parallel string `toml:"parallel"`
}

// ErrNotFound signals no satelle.toml was found walking up from CWD. Callers
// fall back to the zero-value Config (zero-config still works).
var ErrNotFound = errors.New("config: not found")

// resolveUnder joins a possibly-relative path against repoRoot. Absolute paths
// pass through; an empty repoRoot falls back to ".".
func resolveUnder(repoRoot, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	return filepath.Join(repoRoot, p)
}

// ResolveDataDir returns the absolute data dir for repoRoot.
func (c Config) ResolveDataDir(repoRoot string) string {
	p := strings.TrimSpace(c.DataDir)
	if p == "" {
		p = DefaultDataDir
	}
	return resolveUnder(repoRoot, p)
}

// ResolveEditExemptPaths returns the configured [gate] edit_exempt_paths as
// absolute prefixes under repoRoot. Blank entries are DROPPED (a blank prefix
// would classify every edit as exempt and silently disable the gate — see
// withinRoot's fail-open-toward-inside default), and an absolute entry passes
// through unchanged. Returns nil when nothing is configured, so the gate exempts
// nothing and every in-repo edit requires an engaged story (sty_8c3d345c).
func (c Config) ResolveEditExemptPaths(repoRoot string) []string {
	if len(c.Gate.EditExemptPaths) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Gate.EditExemptPaths))
	for _, p := range c.Gate.EditExemptPaths {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, resolveUnder(repoRoot, s))
		}
	}
	return out
}

// ParseEditExemptGlobs is ResolveEditExemptGlobs over the raw committed config
// bytes, for a caller that already read the file. ok is false when the content
// does not decode, so the caller can tell a damaged file from a repo with none.
func ParseEditExemptGlobs(content string) (globs []string, ok bool) {
	var cfg Config
	if _, err := toml.Decode(content, &cfg); err != nil {
		return nil, false
	}
	return cfg.ResolveEditExemptGlobs(), true
}

// ResolveEditExemptGlobs returns trimmed [gate] edit_exempt_globs entries.
// Blank entries are dropped. Patterns are not paths — no repo-root join.
// Returns nil when nothing is configured so the pattern classifier is a no-op.
func (c Config) ResolveEditExemptGlobs() []string {
	if len(c.Gate.EditExemptGlobs) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Gate.EditExemptGlobs))
	for _, p := range c.Gate.EditExemptGlobs {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResolveNoImplementExemptPaths is ResolveEditExemptPaths for the model-rule
// carve-out. Same prefix semantics; a separate list so enabling the model
// rule cannot weaken the engaged-story gate.
func (c Config) ResolveNoImplementExemptPaths(repoRoot string) []string {
	if len(c.Gate.NoImplementExemptPaths) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Gate.NoImplementExemptPaths))
	for _, p := range c.Gate.NoImplementExemptPaths {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, resolveUnder(repoRoot, s))
		}
	}
	return out
}

// ResolveNoImplementExemptGlobs is ResolveEditExemptGlobs for the model-rule
// carve-out.
func (c Config) ResolveNoImplementExemptGlobs() []string {
	if len(c.Gate.NoImplementExemptGlobs) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Gate.NoImplementExemptGlobs))
	for _, p := range c.Gate.NoImplementExemptGlobs {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResolveEngagementParallel returns the seat concurrency mode, defaulting an
// absent/blank value to ParallelNone. Load has already refused anything outside
// the closed set, so callers may switch on the result without re-validating.
func (c Config) ResolveEngagementParallel() string {
	if s := strings.TrimSpace(c.Engagement.Parallel); s != "" {
		return s
	}
	return ParallelNone
}

// ValidateEngagement refuses a seat concurrency mode outside the closed set,
// naming the supported modes. Matching is EXACT — no case folding — so the
// configured surface stays the one the docs state. Blank/absent is valid (it
// means the default). path is named in the message so a multi-file overlay says
// which file to fix.
//
// "all" is deliberately NOT special-cased: it was removed as an option
// (sty_a614a0ea) and is a recorded non-goal, not reserved semantics, so it
// fails through this ordinary unknown-value path.
func ValidateEngagement(c Config, path string) error {
	v := strings.TrimSpace(c.Engagement.Parallel)
	if v == "" || v == ParallelNone || v == ParallelEpic || v == ParallelWorktree {
		return nil
	}
	return fmt.Errorf(
		"config: %s: [engagement] parallel = %q is not a seat concurrency mode — supported modes are %q (one performing story at a time), %q (sibling children of one epic, one working tree per lease) and %q (any stories at once, one working tree per lease)",
		path, v, ParallelNone, ParallelEpic, ParallelWorktree)
}

// ResolveConstitution returns the absolute path to the repo's project
// constitution — <data_dir>/constitution.md — the order-zero doc injected every
// session (epic:session-context). Read directly (it is not an indexed kind).
func (c Config) ResolveConstitution(repoRoot string) string {
	return filepath.Join(c.ResolveDataDir(repoRoot), DefaultConstitutionName)
}

// ResolveDB returns the absolute sqlite database path. An explicit db wins;
// otherwise the home-keyed runtime plane (~/.satelle/<repo-key>/satelle.db),
// falling back to a still-unmigrated legacy <data_dir>/satelle.db when present
// (sty_4660bbe1 / epic:substrate-planes). See ResolveRuntimeDir.
func (c Config) ResolveDB(repoRoot string) string {
	if p := strings.TrimSpace(c.DB); p != "" {
		return resolveUnder(repoRoot, p)
	}
	return c.ResolveRuntimeDB(repoRoot)
}

// ResolveLogsMaxSizeBytes returns the per-file size cap (bytes) for the flat
// evidence logs under the home-keyed runtime logs directory, before a file rolls.
func (c Config) ResolveLogsMaxSizeBytes() int64 {
	kb := c.LogsMaxSizeKB
	if kb <= 0 {
		kb = DefaultLogsMaxSizeKB
	}
	return int64(kb) * 1024
}

// ResolveLogsMaxFiles returns how many rotated flat-log files to keep per log.
func (c Config) ResolveLogsMaxFiles() int {
	if c.LogsMaxFiles > 0 {
		return c.LogsMaxFiles
	}
	return DefaultLogsMaxFiles
}

// ResolveLogLevel returns the log level, defaulting empty to info.
func (c Config) ResolveLogLevel() string {
	if s := strings.TrimSpace(c.LogLevel); s != "" {
		return s
	}
	return DefaultLogLevel
}

// ResolveAuthoredDirs returns kind→absolute-dir for every AuthoredKind, with
// [substrate_roots] overrides applied over the <data_dir>/<kind> default. An
// override may be absolute, placing a kind's source anywhere on disk.
func (c Config) ResolveAuthoredDirs(repoRoot string) map[string]string {
	out := make(map[string]string, len(AuthoredKinds))
	dataDir := c.ResolveDataDir(repoRoot)
	for _, kind := range AuthoredKinds {
		if override := strings.TrimSpace(c.SubstrateRoots[kind]); override != "" {
			// substrate_roots names the PARENT dir; the kind's files live in
			// <override>/<kind>, mirroring satellites' [substrate_roots] semantics.
			out[kind] = resolveUnder(repoRoot, filepath.Join(override, kind))
			continue
		}
		out[kind] = filepath.Join(dataDir, kind)
	}
	return out
}

// RepoRootFromConfigPath derives the repo root (the dir holding .satelle/) from
// a <repo>/.satelle/satelle.toml path. Empty path → "." (CWD).
func RepoRootFromConfigPath(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return "."
	}
	return filepath.Dir(filepath.Dir(configPath))
}

// Load resolves and parses the config, applying the satelle.local.toml overlay.
// It returns the Config, the resolved committed-config path (for repo-root
// derivation), and any error. A missing config is ErrNotFound — callers may
// treat that as "use the zero-value Config" for zero-config operation.
func Load(explicitPath string) (Config, string, error) {
	path, err := ResolvePath(explicitPath)
	if err != nil {
		return Config{}, "", err
	}
	if path == "" {
		return Config{}, "", ErrNotFound
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, path, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	md, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return Config{}, path, &ParseError{Path: path, Err: err}
	}
	if err := undecodedGovernedKeys(md, path); err != nil {
		return Config{}, path, err
	}
	if err := undecodedCommandRuleKeys(md, path); err != nil {
		return Config{}, path, err
	}
	if err := undecodedCommandPolicyKeys(md, path); err != nil {
		return Config{}, path, err
	}
	// Overlay the gitignored per-user satelle.local.toml beside the committed
	// file; its set fields win. Decoding over the populated cfg leaves absent
	// fields untouched. An absent overlay is not an error.
	localPath := filepath.Join(filepath.Dir(path), LocalConfigName)
	rulePath, policyPath := path, path
	if lb, lerr := os.ReadFile(localPath); lerr == nil {
		lmd, derr := toml.Decode(string(lb), &cfg)
		if derr != nil {
			return Config{}, localPath, &ParseError{Path: localPath, Err: derr}
		}
		if err := undecodedGovernedKeys(lmd, localPath); err != nil {
			return Config{}, localPath, err
		}
		if err := undecodedCommandRuleKeys(lmd, localPath); err != nil {
			return Config{}, localPath, err
		}
		if err := undecodedCommandPolicyKeys(lmd, localPath); err != nil {
			return Config{}, localPath, err
		}
		if lmd.IsDefined("gate", "command_policy") {
			policyPath = localPath
		}
		if lmd.IsDefined("gate", "command_rule") {
			rulePath = localPath
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return Config{}, path, fmt.Errorf("config: read %s: %w", localPath, lerr)
	}
	// Closed-set values are refused HERE so every caller of Load shares one
	// answer — a bad mode must not reach the seat as a silent default.
	if err := ValidateEngagement(cfg, path); err != nil {
		return Config{}, path, err
	}
	if err := ValidateGateHandoff(cfg, path); err != nil {
		return Config{}, path, err
	}
	if err := ValidateGateCommandRules(cfg, rulePath); err != nil {
		return Config{}, rulePath, err
	}
	if err := ValidateGateCommandPolicies(cfg, policyPath); err != nil {
		return Config{}, policyPath, err
	}
	if err := validateWorktree(cfg, path); err != nil {
		return Config{}, path, err
	}
	if err := validateTrunk(cfg, path); err != nil {
		return Config{}, path, err
	}
	if err := validateHarness(cfg, path); err != nil {
		return Config{}, path, err
	}
	if err := validateTeam(cfg, path); err != nil {
		return Config{}, path, err
	}
	return cfg, path, nil
}

// ResolvePath finds the committed config: an explicit path, then the
// SATELLE_CONFIG env, then walking up from CWD for .satelle/satelle.toml.
// Returns "" (no error) when none is found.
// FindDataDir walks up from start (empty → CWD) for a `.satelle` DIRECTORY and
// returns its path and whether one was found. This is satelle's one notion of
// "this repo is initialised", and it deliberately keys on the DIRECTORY rather
// than on `.satelle/satelle.toml`: zero-config is supported, so an initialised
// repo may legitimately carry agents.toml and no satelle.toml, and keying on the
// toml would classify it as ungoverned.
//
// resolvePath above walks for the CONFIG file and answers a different question
// ("which config governs here"), so the two intentionally coexist.
func FindDataDir(start string) (string, bool) {
	dir := strings.TrimSpace(start)
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		dir = cwd
	}
	for {
		candidate := filepath.Join(dir, DefaultDataDir)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func ResolvePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := strings.TrimSpace(os.Getenv("SATELLE_CONFIG")); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("config: getwd: %w", err)
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, DefaultDataDir, ConfigName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
