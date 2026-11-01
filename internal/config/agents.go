package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// AgentsConfigName is the per-repo agents-binding file. It lives in the workflows
// dir (.satelle/workflows/agents.toml) beside the two route halves it binds:
// step.toml names a performer and its gates by SECTION NAME, and this file says
// what those names actually run (sty_10f732ed). ActorsConfigName is the
// now-removed legacy filename — it is no longer loaded (sty_7db2ed7d); `satelle
// reindex` warns a repo still carrying it so the rename is enforced rather than
// silently honoured.
const (
	AgentsConfigName = "agents.toml"
	ActorsConfigName = "actors.toml"
	// AgentsConfigDir is the data-dir-relative directory holding AgentsConfigName.
	AgentsConfigDir = "workflows"
)

// AgentsRel is the data-dir-relative slash path of the repo agents layer — the
// spelling every message, server key and sync entry uses so the canonical
// location has ONE spelling.
const AgentsRel = AgentsConfigDir + "/" + AgentsConfigName

// WorkspaceAgentsName is the SYNCED workspace bindings layer (sty_01949949):
// what `satelle sync bindings pull` materialises from the bound team
// workspace's publish catalog. It is a separate file beside the authored
// agents.toml — sync never rewrites authored bytes — and it is a LAYER UNDER
// the repo's own file (see resolveagents.go: a repo table wins field by field;
// a workspace-only table applies). Like agents.toml it is machine configuration
// the directory monitor must not index as a workflow doc.
const WorkspaceAgentsName = "agents.workspace.toml"

// WorkspaceAgentsRel is the data-dir-relative slash path of the workspace layer.
const WorkspaceAgentsRel = AgentsConfigDir + "/" + WorkspaceAgentsName

// WorkspaceAgentsPath is where the synced workspace bindings layer lives.
func WorkspaceAgentsPath(dataDir string) string {
	return filepath.Join(dataDir, AgentsConfigDir, WorkspaceAgentsName)
}

// LocalAgentsName is the MACHINE-LOCAL override of agents.toml (sty_5c56143b):
// the minimal per-machine difference (command paths, profile= into this
// machine's ~/.satelle catalog) that must never travel with the shared file.
// The `.local` segment is what keeps it off every sync path (LocalOnlyPath);
// the managed .gitignore block keeps it out of git. It uses agents.toml's own
// schema and is the TOP layer of the ladder. Only agents.toml has a local twin —
// no other substrate file gains one.
const LocalAgentsName = "agents.local.toml"

// LocalAgentsRel is the data-dir-relative slash path of the machine-local layer.
const LocalAgentsRel = AgentsConfigDir + "/" + LocalAgentsName

// LocalAgentsPath is where the machine-local agents override lives.
func LocalAgentsPath(dataDir string) string {
	return filepath.Join(dataDir, AgentsConfigDir, LocalAgentsName)
}

// AgentLayers is the authored agents layers read from one data dir: Local (this
// machine) over Repo (agents.toml) over Workspace (synced bindings). The *Err
// fields carry a per-layer load failure so a surface that reports each layer
// separately (doctor) can; a layer that failed to load is the zero layer.
type AgentLayers struct {
	Repo, Workspace, Local          AgentsConfig
	RepoErr, WorkspaceErr, LocalErr error
	// MachineLayersExcluded marks layers judged as ANOTHER machine would load
	// them (a push): Local and Workspace were left out on purpose, so a seat the
	// repo writes no command or tools for is completed by each machine's own
	// layer, and its grant can only be judged there.
	MachineLayersExcluded bool
	// ProjectTier is the project tier's agents.toml when it is the Repo layer
	// (sty_8dedc0a4): the team owns agents.toml and the tier is present, so the
	// repo's agents.toml and the workspace layer are not read. Empty otherwise.
	ProjectTier string
	// RepoRoot is the repo the layers were loaded for — the data dir's parent —
	// so a finding about one layer can name the repo it lives in. Empty for
	// layers built in memory.
	RepoRoot string
}

// LoadAgentLayers is the ONE reader of the authored agents layers: every surface
// that resolves or validates agents loads them here, so none can drop the local
// layer. All three are read even when one fails; the returned error joins the
// per-layer failures (nil when all loaded). An absent file is the zero layer.
//
// When the team owns agents.toml and the project tier is present, the tier's
// file is the Repo layer and there is no Workspace layer: a seat or field
// written only in the repo's copy or in agents.workspace.toml cannot govern.
// agents.local.toml stays on top either way.
func LoadAgentLayers(dataDir string) (AgentLayers, error) {
	l := AgentLayers{RepoRoot: filepath.Dir(dataDir)}
	if path, ok := ProjectTierAgentsPath(dataDir); ok {
		l.ProjectTier = path
		l.Repo, l.RepoErr = loadAgentsFile(path)
	} else {
		l.Repo, l.RepoErr = LoadAgents(dataDir)
		l.Workspace, l.WorkspaceErr = LoadWorkspaceAgents(dataDir)
	}
	l.Local, l.LocalErr = LoadLocalAgents(dataDir)
	return l, errors.Join(l.RepoErr, l.WorkspaceErr, l.LocalErr)
}

// Authored folds the repo and local layers into the one view of what this
// machine's agents.toml says — what a validator judges when full resolution
// fails, so a seat named only locally is still checked.
func (l AgentLayers) Authored() AgentsConfig {
	out := AgentsConfig{Defaults: overlayDefaults(l.Repo.Defaults, l.Local.Defaults), Agents: map[string]AgentBinding{}}
	out.Executor, _ = overlayBinding(l.Repo.Executor, nil, l.Local.Executor, SourceLocal)
	out.Reviewer, _ = overlayBinding(l.Repo.Reviewer, nil, l.Local.Reviewer, SourceLocal)
	for _, layer := range []map[string]AgentBinding{l.Repo.Agents, l.Local.Agents} {
		for n := range layer {
			out.Agents[n], _ = overlayBinding(l.Repo.Agents[n], nil, l.Local.Agents[n], SourceLocal)
		}
	}
	out.ModelOrder = layerModelOrder(l.Repo.ModelOrder, l.Local.ModelOrder)
	return out
}

// AgentsPath resolves the repo agents layer, preferring the canonical location
// and falling back to the legacy one beside satelle.toml.
//
// The fallback is what keeps an unconverted repo alive: an initialized repo with
// no loadable agents layer REFUSES to run (requireAgents, sty_d0d6bb67), so a
// hard cutover would brick every repo that had not re-inited. `satelle init`
// relocates the file and reports it; until then the legacy path is read in place.
//
// When NEITHER exists the canonical path is returned with legacy=false, so a
// "missing agents.toml" message names where the file belongs rather than where it
// used to live.
func AgentsPath(dataDir string) (path string, legacy bool) {
	canonical := filepath.Join(dataDir, AgentsConfigDir, AgentsConfigName)
	if _, err := os.Stat(canonical); err == nil {
		return canonical, false
	}
	old := filepath.Join(dataDir, AgentsConfigName)
	if _, err := os.Stat(old); err == nil {
		return old, true
	}
	return canonical, false
}

// Default agent grants — the BOOTSTRAP values a binding's empty fields resolve
// to: the executor drives in-loop (the agent itself); the reviewer runs as an
// isolated agent with a READ-ONLY tool grant (see the satelle-agent-model
// principle — the reviewer is limited to reviewing). They fill blanks INSIDE a
// loaded agents.toml; they are not a substitute for the file itself — an
// initialized repo without a loadable agents.toml refuses to run (the CLI
// bootstrap's requireAgents, sty_d0d6bb67).
const (
	DefaultExecutorCommand = "in-loop"
	// DefaultReviewerCommand is the full claude command template (the canonical
	// seed for a [reviewer] that omits command). It carries the read-only
	// --disallowedTools denylist so the grant is a ceiling. A repo overrides it
	// with its own multi-token command template in agents.toml. Bare single-token
	// presets are no longer accepted on the agents.toml path.
	DefaultReviewerCommand = agentcli.DefaultClaudeCommand
	DefaultReviewerTools   = "Read,Grep,Glob"
)

// Role values for AgentBinding.Role — the declared identity of a binding
// (sty_fc670c9b / epic:agent-invoke-unify). Role is orthogonal to the section
// name: judge-vs-perform is derived from role, not from the literal "reviewer".
const (
	RoleReviewer = "reviewer"
	RoleAgent    = "agent"
)

// Interface values for AgentBinding.Interface — how satelle runs the isolated
// worker subprocess (epic:agent-dispatch-transport). Orthogonal to role:
// command = full argv template (default; any CLI including Claude);
// acp = Agent Client Protocol over stdio (spawn line only; satelle is client);
// stream = Claude stream-json live session (sty_d244fe1b);
// cloud = the step runs once in a provider cloud session whose pushed branch is
// collected into the story worktree (sty_82cffd60). Cloud is a performer-only,
// one-shot transport: it is never a live interface.
// pi-rpc = a pi command run in pi's RPC mode, one-shot (sty_79ce4858); only a
// pi command may use it.
const (
	InterfaceCommand = "command"
	InterfaceACP     = "acp"
	InterfaceStream  = "stream"
	InterfaceCloud   = "cloud"
	InterfacePiRPC   = agentcli.InterfacePiRPC

	// Dispatch marker environment keys identify an isolated performing step to
	// harness hooks. They let the dispatched child use its authored tool grant
	// during the transition's in-flight window without opening that window to
	// the parent driving session. They are an honest-posture boundary, not a
	// sandbox: a process that bypasses or spoofs hooks is outside this contract.
	DispatchAgentEnv = "SATELLE_DISPATCH_AGENT"
	DispatchStepEnv  = "SATELLE_DISPATCH_STEP"
	DispatchItemEnv  = "SATELLE_DISPATCH_ITEM"

	// GateStoryEnv names the story a gate's functional check is judging; the
	// engine exports it into the check's environment (sty_ab93f9a6).
	GateStoryEnv = "SATELLE_GATE_STORY"

	// SpawnEnv marks every process satelle's own buildRequest assembles — the
	// one seam every isolated dispatch, live session and step-summary run
	// through — as satelle's own spawn rather than the in-loop driving session
	// (sty_719c4a7b AC4). Unlike DispatchAgentEnv/StepEnv/ItemEnv it carries no
	// agent/step/item context (a step summary has no PreToolUse edit grant to
	// scope), so it exists only for bindSessionID's isDispatchedProcess check —
	// never for editPermitted, which still needs the three-tuple.
	SpawnEnv = "SATELLE_DISPATCH_SPAWN"

	// Relay marker environment keys identify a rework-relay coder spawn to
	// harness hooks (sty_7567f047). Distinct from SATELLE_DISPATCH_*: the
	// dispatch branch requires InFlight, and the relay runs at a committed
	// status. Same honest-posture boundary as the dispatch markers.
	RelayBindingEnv = "SATELLE_RELAY_BINDING"
	RelayItemEnv    = "SATELLE_RELAY_ITEM"

	// ScratchEnv names the per-dispatch scratch directory every isolated or
	// live agent session receives (sty_e7aaf8b1) — set alongside TMPDIR so an
	// agent never has to be told separately where scratch work goes. A
	// RESERVED key: it overrides any binding-authored [env] value of the
	// same name.
	ScratchEnv = "SATELLE_SCRATCH"
)

// Principles selector tokens for AgentBinding.Principles — which principles ride
// in an isolated agent's briefing (design sty_69fd4e20 §5).
const (
	PrinciplesSession = "session"
	PrinciplesAll     = "all"
	PrinciplesSystem  = "system"
	PrinciplesProject = "project"
	PrinciplesNone    = "none"
)

// Principle residency tags (frontmatter `tags:`). SessionResidencyTag puts a
// principle in the SessionStart context and in every seat whose selector
// includes the session set. SeatResidencyTag puts it in those seats only: a
// cold-start dispatched agent receives it, the in-loop session does not, so it
// costs nothing against a harness's SessionStart byte limit.
const (
	SessionResidencyTag = "principles:session"
	SeatResidencyTag    = "principles:seat"
)

// AgentBinding binds one agent to a backend (how/where it runs) and its grant
// (the tool allowance, and an optional model). Empty fields take the defaults.
//
// Role declares whether the binding is a reviewer (verdict contract) or an agent
// (performer). Principles declares which principles inject into the isolated
// briefing. InjectPrinciples is the DEPRECATED boolean alias for Principles
// (true→session, false→none); Principles wins when both are set.
//
// Interface selects the dispatch transport (epic:agent-dispatch-transport):
// "command" (default), "acp", "stream" or "cloud". Shared grant fields apply to
// all; spawn shape differs.
type AgentBinding struct {
	// Interface is "command" | "acp" | "stream" | "cloud". Empty means command.
	// Unknown values fail at load; cloud only on a role=agent binding.
	Interface string `toml:"interface"`
	// CollectDoc, on an interface=cloud binding, names the story document the
	// collected tip commit's message body is attached under. Empty attaches
	// nothing. What the body holds is the step skill's business, never the
	// binary's. Refused on any other interface.
	CollectDoc string `toml:"collect_doc"`
	// Command is the agent's spawn/template string.
	//   command transport: multi-token full argv template with {system}/{tools}/
	//     {model}/{settings}/{payload} (each its own argv token). Bare single-token
	//     only "in-loop"; bare claude/grok rejected by agentvalidate.
	//   acp transport: ACP stdio spawn only (e.g. "grok agent stdio") — no
	//     {system}/{payload} placeholders (those ride the protocol).
	// Prefer over retired harness= (no runtime fallback; MigrateAgents rewrites).
	Command string `toml:"command"`
	// Harness is retired: still decoded for MigrateAgents; CommandTemplate ignores it.
	Harness string `toml:"harness"`
	Tools   string `toml:"tools"`
	// Model is the model the binding runs, or an ordered comma-separated list
	// ("a,b,c", sty_7bfa980d): a dispatch that ends with no answer, no
	// parseable verdict, a timeout or idle stall, or a rate-limit or
	// unavailability error is re-run once on the next listed model, with every
	// other field unchanged; secondary= applies once the list is spent.
	// Resolution keeps the first entry here and the rest in ModelFallbacks, so
	// every other reader of Model sees exactly one model.
	Model string `toml:"model"`
	// ModelFallbacks is the rest of a comma-listed model=, in order, filled
	// when the binding is resolved (never decoded, never written back). Nil for
	// a single model. See Models.
	ModelFallbacks []string `toml:"-"`
	// Role is "reviewer" | "agent". Empty means inferred from the section name
	// (see ResolvedRole). The binary's only hard determination for a reviewer is
	// the verdict contract; tool grant/model/command are user configuration.
	Role string `toml:"role"`
	// Principles selects which principles inject into the isolated briefing:
	// "session" (default) | "all" | "system" | "project" | "none" | comma-list.
	// Empty defaults to session. inject_principles is retired (MigrateAgents).
	Principles string `toml:"principles"`
	// Env sets environment variables on the dispatched agent's process (layered
	// onto os.Environ, binding keys winning). Each value may reference the [vars]
	// KV via ${NAME}, resolved at CLI wiring time (ResolveAgentEnvs) — how a step
	// points at an alternate model backend, e.g. env = { ANTHROPIC_BASE_URL =
	// "https://api.z.ai/api/anthropic", ANTHROPIC_AUTH_TOKEN = "${GLM_API_KEY}" }.
	// The in-loop executor never execs a child, so its Env is inert (sty_001558ce).
	Env map[string]string `toml:"env"`
	// Timeout bounds ONE dispatch of this binding — a Go duration string (e.g.
	// "45m"). Empty inherits the engine's default dispatch bound. A from-scratch
	// code-writing worker needs a longer window than the 20m default (a real
	// feature was SIGKILLed at exactly 20m — sty_b73c3236), so the bound is authored
	// config, not a compiled constant (sty_446c38b7). Applies to a DISPATCHED named
	// executor; reviewer/summariser gate invocations keep the engine's agent bound.
	Timeout string `toml:"timeout"`
	// IdleTimeout bounds how long ONE dispatch of this binding may go with no
	// REAL event (tool start/end, message, usage — heartbeats excluded) before
	// it is judged stalled and stopped (sty_752c4ef2). A Go duration string
	// (e.g. "5m"). Empty inherits the shared [defaults] idle_timeout, then the
	// binary's shipped default. Unlike Timeout — now an optional HARD ceiling
	// that is unset unless authored — idle_timeout is what actually bounds a
	// dispatch by default: a progressing agent is never cut off by elapsed
	// time alone.
	IdleTimeout string `toml:"idle_timeout"`
	// BusyTimeout bounds how long CPU-liveness may keep a SILENT command-
	// transport run (a one-shot CLI that prints nothing until exit) alive past
	// idle_timeout, measured from its last real output (sty_db62a3b9). A Go
	// duration string; "0" or "off" disables CPU liveness (strict idle_timeout).
	// Empty inherits [defaults] busy_timeout, then the binary's shipped default.
	// Stream and ACP transports ignore it.
	BusyTimeout string `toml:"busy_timeout"`
	// InjectPrinciples is retired (MigrateAgents → principles=). Not used at runtime.
	InjectPrinciples *bool `toml:"inject_principles"`
	// Settings MIRRORS claude's settings.local.json schema (env, model, permissions)
	// verbatim — no derivation, no satelle-specific shape. It is materialised into
	// the {settings} placeholder: ${VAR}-resolved (ResolveAgentEnvs, fail-fast on an
	// unknown var, same as Env), JSON-marshalled, and passed INLINE as
	// `--settings <json>`. Because --settings is CLI-tier it OVERRIDES
	// .claude/settings.local.json, so a binding that authors Settings becomes the
	// authoritative provider/auth/permissions layer for that dispatch — e.g. moving
	// [retrospective]'s GLM env under settings.env fixes its silent clobber by the
	// repo's openrouter settings.local.json (that env block wins over a bare
	// process-env overlay, but never over --settings). A binding with no Settings
	// emits no --settings flag, exactly as an empty Model drops {model}.
	Settings map[string]any `toml:"settings"`
	// Effort is optional reasoning/thinking effort for the binding (sty_657f77b9):
	// e.g. "low" | "medium" | "high". Empty means peer default. Substituted into
	// {effort} on command templates (flag dropped when empty, like {model}) and
	// applied on ACP via session/set_config_option (failure-tolerant).
	Effort string `toml:"effort"`
	// Secondary names another agents.toml binding to retry once when this
	// binding's dispatch fails with a classified rate-limit/unavailable error
	// (sty_5bf61f89). Empty inherits [defaults] secondary. Empty both = no failover.
	Secondary string `toml:"secondary"`
	// Profile names a profile in the MACHINE-WIDE catalog ($SATELLE_HOME/
	// agents.toml) this binding builds on (sty_c7dfeedf). The reference is always
	// EXPLICIT: a catalog profile that happens to share this section's name is
	// never merged in on its own, so adding a profile can never silently change a
	// repo. Repo values on this binding win over the referenced profile field by
	// field; role is identity and must not disagree. A profile may itself set
	// profile= to extend another, with cycles refused at load. See ResolveAgents.
	Profile string `toml:"profile"`
	// Isolation is the operator's declaration about a REVIEWER binding's tool
	// isolation (sty_ef3efb51). The only value is IsolationOperatorAttested: the
	// command offers no tool outside the binding's grant (a script, or a harness
	// satelle has no adapter for). Without it a reviewer no adapter recognises is
	// refused before it starts. It has no effect on a claude or grok binding — the
	// adapter decides those. Any other value fails at load.
	Isolation string `toml:"isolation"`
	// Auth declares how this binding is paid for and authorised: "subscription"
	// (the user's logged-in CLI), "api_key" (a per-call API key) or "org" (an
	// organisation account). It is declared, never detected — sniffing the CLI
	// would break agent-agnosticism — and is recorded on every verdict and
	// performer attempt as auth_mode. Empty resolves to AuthUnrecorded; any other
	// value fails at load. It never changes a price (costview.Pricing owns that).
	Auth string `toml:"auth"`
	// Placement declares where this binding's work runs (sty_7028d996): "local"
	// (default, the dispatching session's machine), "remote" (an ephemeral remote
	// performer paired with the local session over the remote channel) or
	// "team:<path>" (the team remote at <path> on satelle.dev, woken for the story
	// instead of dispatching a performer). Placement is configured here, never in
	// step.toml. Empty means local; any other value fails at load.
	Placement string `toml:"placement"`
	// Remote names the published satelle.dev remote a placement="remote" binding
	// starts a performer node of. Required by, and refused without, that placement.
	Remote string `toml:"remote"`
	// RemotePath names the accepted definition (its remotes/<name> path) the
	// performer node starts from, when the remote's default is not wanted.
	// Optional; valid only with placement="remote".
	RemotePath string `toml:"remote_path"`
}

// IsolationOperatorAttested is the one valid isolation= value.
const IsolationOperatorAttested = "operator-attested"

// The auth= values a binding may declare, and what a binding that declares none
// (or a ledger row written before auth existed) resolves to.
const (
	AuthSubscription = "subscription"
	AuthAPIKey       = "api_key"
	AuthOrg          = "org"
	AuthUnrecorded   = ledger.AuthUnrecorded
)

// Models is the binding's ordered model list: Model's comma-separated entries
// (trimmed, blanks dropped) followed by ModelFallbacks. Nil when no model is
// set. A resolved binding holds one entry in Model; an unresolved one may still
// carry the raw list, which this splits the same way.
func (b AgentBinding) Models() []string {
	var out []string
	for _, m := range strings.Split(b.Model, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return append(out, b.ModelFallbacks...)
}

// splitModelList normalises a comma-listed model= in place: Model keeps the
// first entry and ModelFallbacks the rest. A single model is left exactly as
// written and ModelFallbacks stays nil.
func (b *AgentBinding) splitModelList() {
	if !strings.Contains(b.Model, ",") {
		return
	}
	ms := b.Models()
	b.Model, b.ModelFallbacks = "", nil
	if len(ms) > 0 {
		b.Model = ms[0]
	}
	if len(ms) > 1 {
		b.ModelFallbacks = ms[1:]
	}
}

// ResolvedAuth is the binding's declared auth mode, or AuthUnrecorded when none
// is declared.
func (b AgentBinding) ResolvedAuth() string {
	if a := strings.ToLower(strings.TrimSpace(b.Auth)); a != "" {
		return a
	}
	return AuthUnrecorded
}

// OperatorAttested reports whether the binding declares isolation = "operator-attested".
func (b AgentBinding) OperatorAttested() bool {
	return strings.EqualFold(strings.TrimSpace(b.Isolation), IsolationOperatorAttested)
}

// AgentsDefaults is the optional [defaults] table in agents.toml (sty_5bf61f89).
type AgentsDefaults struct {
	// Secondary is the default fallback binding name for isolated agents when
	// a binding omits secondary= and the primary hits rate-limit/unavailable.
	Secondary string `toml:"secondary"`
	// UseGlobalRoles opts this repo into the machine-wide catalog's [roles]
	// defaults for bindings that name no profile= of their own (sty_c7dfeedf).
	// It is off by default and must be written by hand: without it, tier 3 of the
	// precedence ladder is skipped entirely and the catalog can only reach a
	// binding that explicitly asks for it.
	UseGlobalRoles bool `toml:"use_global_roles"`
	// LiveInterfaceOrder is the preference order ResolveInterface walks for a
	// binding with no interface= that is used LIVE (epic:model-selection child
	// 2) — the constitution's "no opinion as code": the binary only detects
	// which transports a binding's CLI can serve, and this configuration says
	// which capable one it prefers. Empty means the shipped default order
	// (LiveInterfaces()).
	LiveInterfaceOrder []string `toml:"live_interfaces"`
	// IdleTimeout is the repo-wide idle-stall bound (sty_752c4ef2) for a
	// binding that omits its own idle_timeout=. Empty falls to the binary's
	// shipped default.
	IdleTimeout string `toml:"idle_timeout"`
	// BusyTimeout is the repo-wide CPU-liveness cap (sty_db62a3b9) for a
	// binding that omits its own busy_timeout=. Empty falls to the shipped default.
	BusyTimeout string `toml:"busy_timeout"`
}

// LiveInterfaces returns the preference order ResolveInterface walks for a
// live use: the configured live_interfaces when set, else the shipped default
// [stream, acp]. It only orders transports an authored command can serve; it
// never supplies a command.
func (d AgentsDefaults) LiveInterfaces() []string {
	if len(d.LiveInterfaceOrder) > 0 {
		return d.LiveInterfaceOrder
	}
	return []string{InterfaceStream, InterfaceACP}
}

// TimeoutDuration resolves this binding's dispatch bound: the parsed Timeout when
// set, else def. A malformed or non-positive Timeout is an error — LoadAgents
// validates it at load (validateTimeouts) so a dispatch never silently falls back
// on a typo (sty_446c38b7).
func (b AgentBinding) TimeoutDuration(def time.Duration) (time.Duration, error) {
	return parsePositiveDuration(b.Timeout, "timeout", def)
}

// IdleTimeoutDuration resolves this binding's idle-stall bound: the parsed
// IdleTimeout when set, else def. Same fail-fast contract as TimeoutDuration
// (sty_752c4ef2) — a malformed or non-positive idle_timeout is caught at load,
// not at first dispatch.
func (b AgentBinding) IdleTimeoutDuration(def time.Duration) (time.Duration, error) {
	return parsePositiveDuration(b.IdleTimeout, "idle_timeout", def)
}

// BusyTimeoutDuration resolves this binding's CPU-liveness cap: the parsed
// BusyTimeout when set, else def. "0"/"off" yield 0 (CPU liveness disabled).
func (b AgentBinding) BusyTimeoutDuration(def time.Duration) (time.Duration, error) {
	return parseBusyDuration(b.BusyTimeout, def)
}

// parseBusyDuration is parsePositiveDuration that also accepts "0"/"off" as an
// explicit disable (returns 0).
func parseBusyDuration(raw string, def time.Duration) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "off":
		return 0, nil
	}
	return parsePositiveDuration(raw, "busy_timeout", def)
}

// parsePositiveDuration parses raw (a toml duration string) when non-empty,
// requiring a positive result; empty returns def unparsed. field names the
// source in the error so a malformed timeout and idle_timeout are never
// ambiguous.
func parsePositiveDuration(raw, field string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q must be positive", field, raw)
	}
	return d, nil
}

// CommandTemplate resolves the effective command template for this binding.
// Only `command` is read — the deprecated `harness` field is no longer a
// runtime fallback (breaking surface: run `satelle init` to MigrateAgents).
func (b AgentBinding) CommandTemplate() string {
	return b.Command
}

// The placement= values a binding may declare (sty_7028d996). PlacementTeamPrefix
// introduces the team form, "team:<path>".
const (
	PlacementLocal      = "local"
	PlacementRemote     = "remote"
	PlacementTeam       = "team"
	PlacementTeamPrefix = PlacementTeam + ":"
)

// ResolvedPlacement returns the effective placement kind and, for a team
// placement, its path. Empty resolves to local. A "team:" with no path, or an
// unknown value, is returned lowercased as its own kind so checkBindingPlacement
// (the one refusal) and display surfaces see the raw declaration.
func (b AgentBinding) ResolvedPlacement() (kind, path string) {
	raw := strings.TrimSpace(b.Placement)
	lower := strings.ToLower(raw)
	if lower == PlacementRemote {
		return PlacementRemote, ""
	}
	if kind, path, ok := parseLocalOrTeam(raw); ok {
		return kind, path
	}
	return lower, ""
}

// parseLocalOrTeam is the placement grammar a binding and a child pin share:
// "" or "local" is local, "team:<path>" is a team placement with its path. ok is
// false for any other value, including a "team:" with no path.
func parseLocalOrTeam(raw string) (kind, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	switch {
	case lower == "" || lower == PlacementLocal:
		return PlacementLocal, "", true
	case strings.HasPrefix(lower, PlacementTeamPrefix):
		if p := strings.TrimSpace(raw[len(PlacementTeamPrefix):]); p != "" {
			return PlacementTeam, p, true
		}
	}
	return "", "", false
}

// PlacementInLoop is the pin value that makes the driving session perform a
// step. It is a pin only: a binding's placement= never declares it (the
// binding's command being in-loop is config.IsInLoopCommand's question).
const PlacementInLoop = "in-loop"

// ParsePin parses the value of a child's placement pin (the tag under a step's
// placement_tag prefix): "in-loop", "local" or "team:<path>". It returns the
// kind (PlacementInLoop, PlacementLocal or PlacementTeam) and, for a team, its
// path. "remote", an empty value and any unknown value are an error: remote
// placement is binding configuration, never a per-child pin.
func ParsePin(value string) (kind, path string, err error) {
	v := strings.TrimSpace(value)
	if strings.EqualFold(v, PlacementInLoop) {
		return PlacementInLoop, "", nil
	}
	if v != "" {
		if kind, path, ok := parseLocalOrTeam(v); ok {
			return kind, path, nil
		}
	}
	return "", "", fmt.Errorf("placement pin %q is not one of: %s, %s, %s<path> (remote placement is binding configuration, not a pin)",
		value, PlacementInLoop, PlacementLocal, PlacementTeamPrefix)
}

// PlacementLabel is ResolvedPlacement as one string for display: "local",
// "remote" or "team:<path>".
func (b AgentBinding) PlacementLabel() string {
	kind, path := b.ResolvedPlacement()
	if kind == PlacementTeam {
		return PlacementTeamPrefix + path
	}
	return kind
}

// ResolvedInterface returns the effective dispatch transport: command (default),
// acp, or stream. Unknown non-empty values are returned lowercased so
// LoadAgents / agentvalidate can reject them (epic:agent-dispatch-transport).
func (b AgentBinding) ResolvedInterface() string {
	switch strings.ToLower(strings.TrimSpace(b.Interface)) {
	case "", InterfaceCommand:
		return InterfaceCommand
	case InterfaceACP:
		return InterfaceACP
	case InterfaceStream:
		return InterfaceStream
	case InterfaceCloud:
		return InterfaceCloud
	case InterfacePiRPC:
		return InterfacePiRPC
	default:
		return strings.ToLower(strings.TrimSpace(b.Interface))
	}
}

// InterfaceUse says how a binding will be invoked, so ResolveInterface can pick
// the best transport for an unset interface= (epic:model-selection child 2):
// a one-shot dispatch (gate reviewer, planner, edge advisor) always resolves to
// command; a live session (rework relay seat, rework.consult)
// resolves to the CLI's best live transport.
type InterfaceUse int

const (
	// UseOneShot is a single request/response dispatch — a gate verdict, a
	// planner run, a step summary. Always resolves to command.
	UseOneShot InterfaceUse = iota
	// UseLive is a multi-turn session the caller keeps open and talks to.
	UseLive
)

// liveCapableInterface reports whether iface is a live transport the binding's
// AUTHORED command can actually open. Two independent questions, both must
// hold:
//
//   - MECHANISM: which CLI is this? Only Claude speaks the stream-json
//     protocol, so stream is restricted to a "claude" ExecutableToken; any
//     other token is an ACP-capable spawn line and is restricted to acp. This
//     is detection, not opinion — the binary observes what the CLI IS.
//   - SHAPE: can the authored argv actually serve that transport? This is
//     answered by running the SAME construction the runtime opens with
//     (agentcli.RunnerFromBinding), not guessed. A "claude" token is
//     necessary but not sufficient: an authored ONE-SHOT command (e.g.
//     DefaultReviewerCommand's `claude -p --output-format json …
//     --append-system-prompt {system} …`) carries {system}, which
//     newStreamRunner rejects (system/payload ride the live protocol, not
//     argv) — so it correctly reads as not stream-capable rather than being
//     waved through on "first token is claude" and failing (or opening
//     broken) at session-open time.
//
// An empty command is never live-capable: satelle compiles no live default
// command (not for stream, and it cannot guess an ACP spawn line), so an
// unauthored binding is refused rather than resolved to a provider
// (LiveCommandRequiredError).
func liveCapableInterface(command, iface string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	token := ExecutableToken(command)
	switch iface {
	case InterfaceStream:
		if token != "claude" {
			return false
		}
	case InterfaceACP:
		if token == "claude" {
			return false
		}
	default:
		return false
	}
	_, err := agentcli.RunnerFromBinding(iface, command)
	return err == nil
}

// ResolveInterface resolves a binding's effective transport for use, and states
// why:
//
//   - "explicit" — Interface is set; never overridden, whatever use is.
//   - "one-shot default" — a genuine UseOneShot dispatch (gate, planner, edge
//     advisor): always resolves to command.
//   - "live use: in-loop" — UseLive, but the command is the in-loop preset,
//     which has no live transport at all (command is inert there, never a
//     fabricated live one). Distinct from "one-shot default": this IS a live
//     use, it just cannot be one, and the reason says so rather than
//     borrowing the one-shot label.
//   - "live use" — UseLive, resolved to the first live-capable transport in
//     a.Defaults.LiveInterfaces() that can actually open the binding's RAW,
//     undefaulted command (liveCapableInterface reuses the real runner
//     construction and requires a usable default command line for an
//     unauthored command, so "no command yet" only credits a transport that
//     can really open, and an authored one-shot command that cannot serve a
//     live transport is excluded rather than guessed capable from its first
//     token).
//   - "live use: not live-capable" — UseLive, but no configured live
//     transport could serve the command (or, for an empty command, none of
//     them has a usable default line) — falls back to command, and the
//     existing not-live-capable WARN/refusal fires with this reason named
//     explicitly rather than the resolution reading as an ordinary success.
func (a AgentsConfig) ResolveInterface(b AgentBinding, use InterfaceUse) (iface, reason string) {
	if strings.TrimSpace(b.Interface) != "" {
		return b.ResolvedInterface(), "explicit"
	}
	if use == UseOneShot {
		return b.ResolvedInterface(), "one-shot default"
	}
	if IsInLoopCommand(b.Command) {
		return b.ResolvedInterface(), "live use: in-loop"
	}
	if strings.TrimSpace(b.Command) == "" {
		return InterfaceCommand, "live use: no command"
	}
	for _, cand := range a.Defaults.LiveInterfaces() {
		if liveCapableInterface(b.Command, cand) {
			return cand, "live use"
		}
	}
	return InterfaceCommand, "live use: not live-capable"
}

// LiveCommandRequiredError is the refusal for a live binding that authors no
// command and no profile: satelle compiles no live default, so the operator
// must name the spawn line (or a profile that supplies one).
func LiveCommandRequiredError(section string) error {
	return fmt.Errorf("[%s] has no command and no profile= — a live session needs an authored spawn line; set command= to a stream-json CLI (interface=stream) or an ACP agent spawn line (interface=acp), or set profile= to a catalog profile that supplies one", section)
}

// DefaultCommandFor returns the one-shot command template a resolved interface
// supplies when a binding has no command of its own. Live transports (stream,
// acp) have no compiled default — the spawn line is authored configuration.
func DefaultCommandFor(iface string) string {
	switch iface {
	case InterfaceCommand:
		return DefaultReviewerCommand
	default:
		return ""
	}
}

// EffectiveBinding returns a copy of b with Interface resolved for use
// (ResolveInterface) and, when b.Command is empty, Command filled from the
// resolved interface's default. This is the SINGLE seam live callers
// (agentstep.Engine.OpenSessionAs) and `satelle agent validate` share, so
// neither can resolve or report a transport the other would not open
// (sty_8e0b29a0's validate/runtime invariant, extended to the derived default).
func (a AgentsConfig) EffectiveBinding(b AgentBinding, use InterfaceUse) AgentBinding {
	iface, _ := a.ResolveInterface(b, use)
	eb := b
	eb.Interface = iface
	if strings.TrimSpace(eb.Command) == "" && use == UseOneShot {
		eb.Command = DefaultCommandFor(iface)
	}
	return eb
}

// RawBinding returns the binding named name exactly as authored — the
// executor/reviewer sections or an [<name>] entry — with NO command
// defaulting, so EffectiveBinding can tell "no command yet" (every live
// transport a candidate) from "already claude-shaped". Unlike NamedBinding,
// this is not a dispatch resolver: it is the raw material ResolveInterface,
// EffectiveBinding and LiveBinding resolve from — exported so a caller that
// needs the resolved reason alongside the effective binding (e.g. `satelle
// agent validate`) can call ResolveInterface itself on the same raw value.
func (a AgentsConfig) RawBinding(name string) (AgentBinding, bool) {
	switch name {
	case "executor":
		return a.Executor, true
	case "reviewer":
		return a.Reviewer, true
	}
	b, ok := a.Agents[name]
	return b, ok
}

// LiveRawBinding is RawBinding plus each role's OWN baseline default — the
// same one its one-shot resolver (ExecutorBinding / ReviewerBinding) would
// apply — so a caller that needs BOTH the resolved reason (ResolveInterface)
// and the effective binding (EffectiveBinding) for a live use of "executor" or
// "reviewer" computes them from the identical starting point LiveBinding uses,
// rather than re-deriving from the bare RawBinding and disagreeing with it
// (sty_119f6fda: `satelle agent validate`'s grant loop did exactly that).
//
// "executor" and "reviewer" are reachable as a live-use name (rework.consult=
// executor or =reviewer is a legitimate config, checked by agentvalidate's
// rework-alloc rule) but RawBinding deliberately returns them with NO role
// defaulting, so this layers each role's default in first, before
// ResolveInterface/EffectiveBinding ever see Interface/Command:
//
//   - reviewer: an empty Tools grant becomes DefaultReviewerTools (read-only),
//     matching the ceiling ReviewerBinding() gives the one-shot gate path — a
//     consulted reviewer must not silently lose that ceiling just because it
//     is opened live instead of dispatched as a gate.
//   - executor: an empty Command becomes DefaultExecutorCommand ("in-loop").
//     Unlike every other binding, an unset executor command has an ESTABLISHED
//     meaning already (ExecutorBinding: "the driving agent itself") — it is
//     not "no command yet, pick me a live default". Defaulting it here BEFORE
//     ResolveInterface/EffectiveBinding lets IsInLoopCommand see "in-loop" and
//     resolve/report it as such, instead of the empty command reading as
//     "every live transport is a candidate" and reporting (or opening) a real
//     Claude subprocess for what is configured to be the driving session
//     itself.
func (a AgentsConfig) LiveRawBinding(name string) (AgentBinding, bool) {
	b, ok := a.RawBinding(name)
	if !ok {
		return AgentBinding{}, false
	}
	switch name {
	case "reviewer":
		if b.Tools == "" {
			b.Tools = DefaultReviewerTools
		}
	case "executor":
		if b.Command == "" {
			b.Command = DefaultExecutorCommand
		}
	}
	return b, true
}

// LiveBinding resolves name's binding as EffectiveBinding(UseLive) over
// LiveRawBinding(name) — the transport and command a live session
// (agentstep.Engine.OpenSessionAs, and so the rework
// relay) actually opens, and what `satelle agent validate` reports for a live
// use of that binding.
func (a AgentsConfig) LiveBinding(name string) (AgentBinding, bool) {
	b, ok := a.LiveRawBinding(name)
	if !ok {
		return AgentBinding{}, false
	}
	return a.EffectiveBinding(b, UseLive), true
}

// OneShotBinding is LiveBinding's one-shot counterpart: name's raw binding with
// each role's baseline default (an unset [executor] command is in-loop), resolved
// for a single headless run (EffectiveBinding, UseOneShot). The headless drive
// performs an executor-allocated step through it.
func (a AgentsConfig) OneShotBinding(name string) (AgentBinding, bool) {
	b, ok := a.LiveRawBinding(name)
	if !ok {
		return AgentBinding{}, false
	}
	return a.EffectiveBinding(b, UseOneShot), true
}

// IsACP reports whether this binding uses the ACP transport.
func (b AgentBinding) IsACP() bool {
	return b.ResolvedInterface() == InterfaceACP
}

// IsStream reports whether this binding uses the stream-json transport.
func (b AgentBinding) IsStream() bool {
	return b.ResolvedInterface() == InterfaceStream
}

// ResolvedRole returns the binding's effective role: the declared Role when set
// to reviewer|agent (case-insensitive), else inferred from the section name
// (section "reviewer" → reviewer, otherwise agent). section is the agents.toml
// table name ([reviewer], [planner], …).
func ResolvedRole(section string, b AgentBinding) string {
	switch strings.ToLower(strings.TrimSpace(b.Role)) {
	case RoleReviewer:
		return RoleReviewer
	case RoleAgent:
		return RoleAgent
	}
	if strings.EqualFold(strings.TrimSpace(section), RoleReviewer) {
		return RoleReviewer
	}
	return RoleAgent
}

// RoleInferred reports whether role was not declared and will be inferred from
// the section name — used by agent validate/show to warn the operator to declare it.
func RoleInferred(b AgentBinding) bool {
	r := strings.ToLower(strings.TrimSpace(b.Role))
	return r != RoleReviewer && r != RoleAgent
}

// GrantsContextChannel reports whether a binding's tool grant gives a DISPATCHED
// agent a context channel — the pull-context contract (sty_47d31300). A dispatched
// performer starts with no conversation history and reconstructs its context by
// PULLING the story, its documents, and the ledger. Two channels satisfy it
// (sty_565a0202):
//
//  1. satelle CLI via shell: `Bash(satelle…)`, broad `Bash`/`Bash(*)`, or `*`.
//  2. Disk reads of story documents under the home-keyed runtime plane
//     (~/.satelle/<repo-key>/stories/<id>/) via the adapter's own read tool
//     (agentcli.ContextReadTools: Claude `Read`, pi `read`, grok-native
//     `read_file`, used when headless Grok cannot enable run_terminal_command).
//     adapter is the binding's agentcli.AdapterName, so a read tool of one
//     adapter does not count on another. The in-repo `.satelle/stories/` path
//     is obsolete (sty_58fa970e).
//
// A grant with neither channel leaves the agent silently context-starved, so
// dispatch refuses it loudly.
//
// REVIEWERS need no channel — satelle injects the attachments into the transition
// payload's docs array and reviewer bindings never reach dispatch. A shell grant
// on a reviewer is therefore unused capability, not a requirement.
//
// This is the SINGLE owner of the rule (sty_87c0ef37): the runtime dispatch
// refusal and `satelle agent validate` both call it, so they cannot disagree
// about any grant string. It carries its own quote-stripping tokenizer rather
// than reusing splitList so that a quoted TOML token ("Bash(satelle:*)") is
// judged identically on both paths.
func GrantsContextChannel(adapter, tools string) bool {
	readTools := agentcli.ContextReadTools(adapter)
	for _, raw := range strings.Split(tools, ",") {
		t := strings.Trim(strings.TrimSpace(raw), `"'`)
		if t == "" {
			continue
		}
		if t == "*" || t == "Bash" || t == "Bash(*)" || strings.HasPrefix(t, "Bash(satelle") {
			return true
		}
		for _, r := range readTools {
			if t == r {
				return true
			}
		}
	}
	return false
}

// BindingGrantsContextChannel is GrantsContextChannel for a binding: the adapter is
// the one behind its command, so callers never classify it themselves.
func BindingGrantsContextChannel(b AgentBinding) bool {
	return GrantsContextChannel(agentcli.AdapterName(b.CommandTemplate()), b.Tools)
}

// NeedsContextChannel reports whether a binding allocated to a performing node
// must grant a pull-context channel. An in-loop binding is performed by the
// driving session with full context; a cloud binding is handed the whole payload
// in its prompt and has no satelle CLI or store to pull from. Neither is
// dispatched as a context-pulling child, so neither needs a grant. This is the
// one owner of that exemption: the dispatch refusal and `satelle agent validate`
// both ask it, then judge the grant with GrantsContextChannel.
//
// A binding whose adapter reads satelle's material by absolute path with its own
// read tool (agentcli.ReadsMaterialByPath) is exempt too: its channel is the
// adapter's, not a tools grant, so no grant string can be missing.
func NeedsContextChannel(b AgentBinding) bool {
	cmd := b.CommandTemplate()
	return !IsInLoopCommand(cmd) && b.ResolvedInterface() != InterfaceCloud &&
		!agentcli.ReadsMaterialByPath(agentcli.AdapterName(cmd))
}

// ShellGrantToken returns the first token in a tool grant that confers shell
// access, or "" when none does. It is a REPORTING helper — it names which token
// a finding is about — and is deliberately not the channel decision, which
// GrantsContextChannel alone owns.
func ShellGrantToken(tools string) string {
	for _, raw := range strings.Split(tools, ",") {
		t := strings.Trim(strings.TrimSpace(raw), `"'`)
		if t == "" {
			continue
		}
		if t == "*" || t == "Bash" || t == "Bash(*)" || strings.HasPrefix(t, "Bash(") {
			return t
		}
	}
	return ""
}

// IsInLoopCommand reports whether a binding command is the in-loop preset (single
// token "in-loop", case-insensitive). An in-loop binding is performed by the
// driving session and never dispatched as a child, so it needs no context
// channel and cannot produce an isolated verdict. Empty command is NOT in-loop:
// tests and bootstrap leave command blank and wire a runner directly.
func IsInLoopCommand(cmd string) bool {
	fields := strings.Fields(strings.TrimSpace(cmd))
	return len(fields) == 1 && strings.EqualFold(fields[0], "in-loop")
}

// ResolvedPrinciples returns the principles selector: Principles when set,
// else session. The deprecated inject_principles field is no longer a runtime
// fallback (breaking surface: run `satelle init` to MigrateAgents).
func (b AgentBinding) ResolvedPrinciples() string {
	if p := strings.TrimSpace(b.Principles); p != "" {
		return normalizePrinciplesSelector(p)
	}
	return PrinciplesSession
}

// InjectsPrinciples reports whether this binding injects any principles (and the
// constitution order-zero block) into the isolated agent's context — true unless
// the resolved selector is none.
func (b AgentBinding) InjectsPrinciples() bool {
	return b.ResolvedPrinciples() != PrinciplesNone
}

// normalizePrinciplesSelector lowercases, trims, and re-joins comma-list tokens.
func normalizePrinciplesSelector(s string) string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return PrinciplesSession
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out, ",")
}

// AgentsConfig is the on-disk shape at .satelle/workflows/agents.toml — the agents layer.
// Every field is optional; the *Binding resolvers supply today's defaults, so
// the zero value (and an absent file) is the current behaviour. Agents holds
// OPTIONAL named agents (beyond the executor/reviewer roles) declared as flat
// top-level [<name>] sections — consistent with [executor]/[reviewer] — or the
// legacy nested [agents.<name>] (still read for back-compat). A workflow node may
// allocate a step to one, and a named agent is ALWAYS isolated (see
// satelle-agent-model). LoadAgents does the classification; the toml tag here is
// retained only for the legacy nested form.
type AgentsConfig struct {
	Defaults AgentsDefaults          `toml:"defaults"`
	Executor AgentBinding            `toml:"executor"`
	Reviewer AgentBinding            `toml:"reviewer"`
	Agents   map[string]AgentBinding `toml:"agents"`
	// ModelOrder is the [model_order] table (sty_4fde0a50): one ordered list
	// of models per executable token (claude, grok). It is what an
	// unpinned dispatch with no same-executable inherited model follows.
	ModelOrder map[string][]ModelRank `toml:"model_order"`
}

// ModelRank is one rank in an executable's model order: the names that are the
// same model (an alias and its resolved id). The first name is the value
// applied. In agents.toml a rank is a plain string, or an array of strings
// when several names share the rank.
type ModelRank []string

// UnmarshalTOML decodes a rank written as a string or an array of strings.
func (r *ModelRank) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case string:
		*r = ModelRank{v}
	case []any:
		out := make(ModelRank, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("model_order rank entries must be strings, got %T", e)
			}
			out = append(out, s)
		}
		*r = out
	default:
		return fmt.Errorf("model_order rank must be a string or an array of strings, got %T", data)
	}
	return nil
}

// First returns the name applied for this rank, or "" when the rank is empty.
func (r ModelRank) First() string {
	for _, n := range r {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return ""
}

// OrderFor returns the model order configured for executable — an exact,
// case-insensitive match on the executable token and nothing else, so one
// executable's list is never applied to another and an unrecognised executable
// gets nil rather than a Claude default.
func (a AgentsConfig) OrderFor(executable string) []ModelRank {
	exe := strings.TrimSpace(executable)
	if exe == "" {
		return nil
	}
	for k, v := range a.ModelOrder {
		if strings.EqualFold(strings.TrimSpace(k), exe) {
			return v
		}
	}
	return nil
}

// DefaultIdleTimeout is the shipped default idle-stall bound (sty_752c4ef2):
// long enough for a slow model's thinking pause between tool calls, short
// enough to catch a genuinely hung process. The single source of truth for
// every caller that resolves a binding's idle_timeout with no override —
// agentstep.DefaultIdleTimeout re-exports it for callers already importing
// that package; verb (which agentstep itself imports) uses this directly to
// avoid an import cycle.
const DefaultIdleTimeout = 5 * time.Minute

// ResolveIdleTimeout resolves b's idle-stall bound against the shared
// [defaults] table: b.IdleTimeout wins, else Defaults.IdleTimeout, else def
// (the binary's shipped default) — the same binding-then-defaults ladder
// ResolveSecondary walks (sty_752c4ef2).
func (a AgentsConfig) ResolveIdleTimeout(b AgentBinding, def time.Duration) (time.Duration, error) {
	if b.IdleTimeout != "" {
		return b.IdleTimeoutDuration(def)
	}
	if a.Defaults.IdleTimeout != "" {
		return parsePositiveDuration(a.Defaults.IdleTimeout, "idle_timeout", def)
	}
	return def, nil
}

// DefaultBusyTimeout is the shipped CPU-liveness cap (sty_db62a3b9): how long a
// silent command-transport run whose process tree keeps consuming CPU stays
// exempt from idle_timeout, measured from its last real output. It is the
// backstop against a runaway spin when no hard timeout is set.
const DefaultBusyTimeout = 60 * time.Minute

// ResolveBusyTimeout resolves b's CPU-liveness cap: b.BusyTimeout wins, else
// Defaults.BusyTimeout, else def. 0 means disabled ("0"/"off").
func (a AgentsConfig) ResolveBusyTimeout(b AgentBinding, def time.Duration) (time.Duration, error) {
	if b.BusyTimeout != "" {
		return b.BusyTimeoutDuration(def)
	}
	return parseBusyDuration(a.Defaults.BusyTimeout, def)
}

// ResolveSecondary returns the fallback binding for section/b when secondary is
// configured (per-binding wins over [defaults] secondary). ok is false when
// unconfigured or the named binding is missing (sty_5bf61f89).
func (a AgentsConfig) ResolveSecondary(section string, b AgentBinding) (sec AgentBinding, name string, ok bool) {
	name = strings.TrimSpace(b.Secondary)
	if name == "" {
		name = strings.TrimSpace(a.Defaults.Secondary)
	}
	if name == "" {
		return AgentBinding{}, "", false
	}
	if strings.EqualFold(name, strings.TrimSpace(section)) {
		return AgentBinding{}, "", false // refuse self-loop
	}
	switch strings.ToLower(name) {
	case "reviewer":
		rb := a.Reviewer
		if rb.CommandTemplate() == "" {
			rb.Command = DefaultReviewerCommand
		}
		return rb, "reviewer", true
	case "executor":
		return a.Executor, "executor", true
	}
	nb, found := a.NamedBinding(name)
	if !found {
		return AgentBinding{}, name, false
	}
	return nb, name, true
}

// NamedBinding resolves an optional named agent declared as a flat top-level
// [<name>] section (or the legacy nested [agents.<name>]). ok is false when none is
// declared, so a workflow node that allocates a step to an absent agent degrades
// gracefully to the in-loop executor. A named agent is always isolated; an unset
// command defaults to the isolated claude preset.
func (a AgentsConfig) NamedBinding(name string) (AgentBinding, bool) {
	b, ok := a.Agents[name]
	if !ok {
		return AgentBinding{}, false
	}
	b.Command = b.CommandTemplate()
	if b.Command == "" {
		b.Command = DefaultReviewerCommand
	}
	return b, true
}

// ReviewerBinding resolves the reviewer agent's backend and grant, defaulting to
// an isolated agent with the read-only tool grant. The grant travels with the
// binding, so the reviewer's read-only limit holds whatever the backend.
func (a AgentsConfig) ReviewerBinding() AgentBinding {
	b := a.Reviewer
	b.Command = b.CommandTemplate()
	if b.Command == "" {
		b.Command = DefaultReviewerCommand
	}
	if b.Tools == "" {
		b.Tools = DefaultReviewerTools
	}
	return b
}

// ExecutorBinding resolves the executor agent's backend, defaulting to in-loop
// (the driving agent itself).
func (a AgentsConfig) ExecutorBinding() AgentBinding {
	b := a.Executor
	b.Command = b.CommandTemplate()
	if b.Command == "" {
		b.Command = DefaultExecutorCommand
	}
	return b
}

// LoadAgents reads the agents layer through AgentsPath — the canonical
// <dataDir>/workflows/agents.toml, or the legacy <dataDir>/agents.toml while a
// repo is unconverted. The legacy actors.toml is no longer read (sty_7db2ed7d);
// an absent agents.toml yields the zero AgentsConfig — defaults via the *Binding
// resolvers — and a nil error. Absence is judged by the CALLER: the CLI bootstrap
// treats a missing file in an initialized repo as broken and refuses to run
// (requireAgents, sty_d0d6bb67); pre-init surfaces (nothing to load yet) keep the
// zero-config bootstrap.
func LoadAgents(dataDir string) (AgentsConfig, error) {
	path, _ := AgentsPath(dataDir)
	return loadAgentsFile(path)
}

// loadAgentsFile reads one agents.toml; an absent file is the zero layer.
func loadAgentsFile(path string) (AgentsConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AgentsConfig{}, nil
		}
		return AgentsConfig{}, err
	}
	return loadAgentsBody(string(b))
}

// loadAgentsBody classifies an agents-layer body through the ONE decoder
// (decodeAgents, live form: nested [agents.<name>] ignored — MigrateAgents
// flattens it on init) and applies the fail-fast load checks.
func loadAgentsBody(body string) (AgentsConfig, error) {
	ac, err := decodeAgents(body, false)
	if err != nil {
		return AgentsConfig{}, err
	}
	if err := ac.validateTimeouts(); err != nil {
		return AgentsConfig{}, err
	}
	if err := ac.validateInterfaces(); err != nil {
		return AgentsConfig{}, err
	}
	return ac, nil
}

// LoadWorkspaceAgents reads the synced workspace bindings layer
// (WorkspaceAgentsPath). An absent file is the zero config and a nil error, so
// a repo that never pulled one resolves byte-identically to today; a present
// but malformed file is an error, so a broken layer fails loud rather than
// silently dropping out of the ladder (sty_01949949).
func LoadWorkspaceAgents(dataDir string) (AgentsConfig, error) {
	b, err := os.ReadFile(WorkspaceAgentsPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return AgentsConfig{}, nil
		}
		return AgentsConfig{}, err
	}
	ac, err := loadAgentsBody(string(b))
	if err != nil {
		return AgentsConfig{}, fmt.Errorf("%s: %w", WorkspaceAgentsRel, err)
	}
	return ac, nil
}

// LoadLocalAgents reads the machine-local override (LocalAgentsPath). Absent is
// the zero config and a nil error, so a machine with no override resolves
// byte-identically to before; an unreadable or malformed file is an error naming
// it (sty_5c56143b).
func LoadLocalAgents(dataDir string) (AgentsConfig, error) {
	b, err := os.ReadFile(LocalAgentsPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return AgentsConfig{}, nil
		}
		return AgentsConfig{}, fmt.Errorf("%s: %w", LocalAgentsRel, err)
	}
	ac, err := loadAgentsBody(string(b))
	if err != nil {
		return AgentsConfig{}, fmt.Errorf("%s: %w", LocalAgentsRel, err)
	}
	return ac, nil
}

// ExecutableToken returns the program a command template would spawn — its
// first whitespace-separated token — or "" for the in-loop / empty command
// (nothing is spawned). It is what local resolution looks up on PATH
// (sty_01949949 AC3): a workspace-published binding names `claude`, and THIS
// machine decides whether `claude` exists.
func ExecutableToken(command string) string {
	if IsInLoopCommand(command) {
		return ""
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ExecutableToken is ExecutableToken over the binding's effective command template.
func (b AgentBinding) ExecutableToken() string {
	return ExecutableToken(b.CommandTemplate())
}

// SpawnedExecutables returns the lowercased base names of the programs the
// executor, the reviewer and every named binding would spawn, read through the
// seat resolvers so an unset command counts as its default. Call it on an
// already-resolved layer (LoadEffectiveAgents) so profile= commands are filled.
func (a AgentsConfig) SpawnedExecutables() map[string]bool {
	out := map[string]bool{}
	add := func(b AgentBinding) {
		if tok := b.ExecutableToken(); tok != "" {
			out[strings.ToLower(filepath.Base(tok))] = true
		}
	}
	add(a.ExecutorBinding())
	add(a.ReviewerBinding())
	for name := range a.Agents {
		if b, ok := a.NamedBinding(name); ok {
			add(b)
		}
	}
	return out
}

// validateTimeouts fails fast on a malformed or non-positive [<section>] timeout
// so a typo is caught at load — consistent with the rest of agents.toml's
// fail-fast bootstrap — rather than surfacing only when that binding dispatches
// (sty_446c38b7).
func (a AgentsConfig) validateTimeouts() error {
	check := func(section string, b AgentBinding) error {
		return checkBindingTimeout(AgentsConfigName, section, b)
	}
	if err := check("executor", a.Executor); err != nil {
		return err
	}
	if err := check("reviewer", a.Reviewer); err != nil {
		return err
	}
	for name, b := range a.Agents {
		if err := check(name, b); err != nil {
			return err
		}
	}
	if a.Defaults.IdleTimeout != "" {
		if _, err := parsePositiveDuration(a.Defaults.IdleTimeout, "idle_timeout", 0); err != nil {
			return fmt.Errorf("%s [defaults] idle_timeout: %w", AgentsConfigName, err)
		}
	}
	if _, err := parseBusyDuration(a.Defaults.BusyTimeout, 0); err != nil {
		return fmt.Errorf("%s [defaults] busy_timeout: %w", AgentsConfigName, err)
	}
	return nil
}

// validateInterfaces fails fast on an unknown interface= value
// (epic:agent-dispatch-transport), or an unknown token in [defaults]
// live_interfaces (epic:model-selection child 2). Empty is fine (defaults to
// command / the shipped live order).
func (a AgentsConfig) validateInterfaces() error {
	check := func(section string, b AgentBinding) error {
		if err := checkBindingIsolation(AgentsConfigName, section, b); err != nil {
			return err
		}
		if err := checkBindingAuth(AgentsConfigName, section, b); err != nil {
			return err
		}
		if err := checkBindingPlacement(AgentsConfigName, section, b); err != nil {
			return err
		}
		return checkBindingInterface(AgentsConfigName, section, b)
	}
	if err := check("executor", a.Executor); err != nil {
		return err
	}
	if err := check("reviewer", a.Reviewer); err != nil {
		return err
	}
	for name, b := range a.Agents {
		if err := check(name, b); err != nil {
			return err
		}
	}
	for _, raw := range a.Defaults.LiveInterfaceOrder {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case InterfaceStream, InterfaceACP:
		default:
			return fmt.Errorf("%s [defaults] live_interfaces %q: want %q or %q",
				AgentsConfigName, raw, InterfaceStream, InterfaceACP)
		}
	}
	return nil
}
