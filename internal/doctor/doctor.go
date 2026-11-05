// Package doctor answers one question — is satelle ready to govern this
// repository? — by COMPOSING the validation authorities that already exist
// (sty_e9da28e2).
//
// It decides nothing new. Every rule it reports comes from an existing owner:
// `agentvalidate` for bindings, profile resolution, workflow allocations and
// lifecycle hooks; `structure` for authored-substrate contracts;
// `agentstep.WorkflowConsistency` for cross-workflow ambiguity; `config` for
// resolution and provenance. Doctor's contribution is composition, classification
// into stable `health` identifiers, and rendering — never a gate, never a repo
// opinion. A rule that lived only here could not be changed without a binary
// release, which is exactly what the constitution forbids.
//
// The deterministic pass makes no paid or network model call. Live provider
// probes are opt-in (see live.go) and bounded.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

// DefaultLiveTimeout bounds ONE live provider probe.
type Opts struct {
	// RepoRoot is the repository being checked; DataDir is its authored plane.
	// An empty DataDir is derived as <RepoRoot>/.satelle.
	RepoRoot string
	DataDir  string
	// GlobalDir is the machine home used to read recorded sync state.
	// Empty means config.GlobalDir().
	GlobalDir string
	// ExcludeMachineAgentLayers judges the agents layer as ANOTHER machine would
	// load it: this machine's agents.local.toml and the synced agents.workspace.toml
	// are left out of the resolution, so an allocation that resolves only through
	// them fails here as it would there. A push sets it (sty_89b9df20); the machine
	// catalog stays in, as every machine has its own.
	ExcludeMachineAgentLayers bool
	// Process and ProcessRoot are the process-of-record config and root, with
	// RepoRoot as the invoking tree. When Process is set and the invoking tree is
	// a linked worktree whose own copy of the authored process differs from the
	// main tree's, Check reports it (sty_d6e209aa). Nil Process skips the check.
	Process     *config.Config
	ProcessRoot string
	// probe overrides the live prober in tests. Nil uses the real one.
	probe func(ctx context.Context, g agentvalidate.Grant, timeout time.Duration) health.Findings
}

// DefaultLiveTimeout is the per-probe deadline when Opts.LiveTimeout is zero.
const DefaultLiveTimeout = 20 * time.Second

// Report is one repository's health.
type Report struct {
	Repo     string                         `json:"repo"`
	OK       bool                           `json:"ok"`
	Findings health.Findings                `json:"findings"`
	Grants   []agentvalidate.Grant          `json:"grants,omitempty"`
	Gates    []agentvalidate.GateAllocation `json:"gates,omitempty"`
	Sources  map[string]map[string]string   `json:"sources,omitempty"`
	Env      map[string][]EnvKey            `json:"env,omitempty"`
	Live     bool                           `json:"live"`
}

// EnvKey names one binding environment key and whether it resolved. VALUES are
// never carried: an env value may be a credential, and doctor's payload is
// printed, piped, and pasted into issues.
type EnvKey struct {
	Key      string `json:"key"`
	Resolved bool   `json:"resolved"`
}

// resolveDataDir derives the authored plane for a repo root.
func (o Opts) resolveDataDir() string {
	if d := strings.TrimSpace(o.DataDir); d != "" {
		return d
	}
	return filepath.Join(o.RepoRoot, config.DefaultDataDir)
}

// authoredRoots is the authored roots the process config resolves — the same
// owner the other loaders read, so a [substrate_roots] override and a project
// tier are honoured rather than assumed to be <data dir>/<kind>. With no process
// config it falls back to <data dir>/<kind> and that repo's tier.
func (o Opts) authoredRoots(dataDir string) config.AuthoredRoots {
	if o.Process != nil && strings.TrimSpace(o.ProcessRoot) != "" {
		if r := config.ResolveAuthoredRoots(*o.Process, o.ProcessRoot, ""); r.Dirs["workflows"] != "" {
			return r
		}
	}
	return config.AuthoredRootsOf(dataDir)
}

// wiringConfig is the configuration BindingWiring resolves gate wiring from:
// the process of record when set (the source the dispatch guard uses), else the
// repo's own satelle.toml, else the zero config, which still yields the
// embedded wiring.
func (o Opts) wiringConfig(dataDir string) config.Config {
	if o.Process != nil {
		return *o.Process
	}
	if cfg, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err == nil {
		return cfg
	}
	return config.Config{}
}

// Check runs the deterministic readiness pass for one repository, plus the
// opt-in live probes when Opts.Live is set. It never returns an error: an
// unreadable input is itself a finding, because a diagnostic that refuses to
// report is worse than one reporting a defect.
func Check(ctx context.Context, o Opts) Report {
	dataDir := o.resolveDataDir()
	rep := Report{Repo: o.RepoRoot}

	// 1. The effective agents layer, resolved against the machine-wide catalog,
	//    with every binding/allocation/hook rule owned by agentvalidate.
	layers, _ := config.LoadAgentLayers(dataDir)
	if o.ExcludeMachineAgentLayers {
		layers.Workspace, layers.WorkspaceErr = config.AgentsConfig{}, nil
		layers.Local, layers.LocalErr = config.AgentsConfig{}, nil
		layers.MachineLayersExcluded = true
	}
	if agentsErr := layers.RepoErr; agentsErr != nil {
		rep.Findings = append(rep.Findings, health.Error(health.IDAgentsLoad, "Unreadable agents layer",
			fmt.Sprintf("%s/%s: %v", config.DefaultDataDir, config.AgentsRel, agentsErr)).
			WithRemediation("fix the file, or delete it and run `satelle init` to reseed the default").
			About(config.DefaultDataDir+"/"+config.AgentsRel))
	}
	global, globalErr := config.LoadGlobalAgents()
	if globalErr != nil {
		rep.Findings = append(rep.Findings, health.Error(health.IDAgentsProfileBroken, "Unreadable profile catalog",
			globalErr.Error()).
			WithRemediation("fix "+config.GlobalAgentsLabel+"; it is machine-wide EXECUTION configuration only").
			About(config.GlobalAgentsLabel))
	}
	// The synced workspace bindings layer (sty_01949949) is judged with the
	// repo's file so doctor and `satelle agent validate` describe the same
	// effective bindings. Absent is the zero layer; unreadable is a finding.
	if wsErr := layers.WorkspaceErr; wsErr != nil {
		rep.Findings = append(rep.Findings, health.Error(health.IDAgentsLoad, "Unreadable workspace bindings layer",
			fmt.Sprintf("%s/%s: %v", config.DefaultDataDir, config.WorkspaceAgentsRel, wsErr)).
			WithRemediation("re-run `satelle sync bindings pull`, or delete the file to drop the layer").
			About(config.DefaultDataDir+"/"+config.WorkspaceAgentsRel))
	}
	// This machine's override (sty_5c56143b) is judged with the rest, so doctor
	// and `satelle agent validate` describe the same effective bindings.
	if localErr := layers.LocalErr; localErr != nil {
		rep.Findings = append(rep.Findings, health.Error(health.IDAgentsLoad, "Unreadable machine-local agents layer",
			fmt.Sprintf("%s/%s: %v", config.DefaultDataDir, config.LocalAgentsRel, localErr)).
			WithRemediation("fix the file, or delete it to drop this machine's override").
			About(config.DefaultDataDir+"/"+config.LocalAgentsRel))
	}

	// Two workflow sets, deliberately: ALLOCATION checks judge everything that
	// governs (authored files plus the embedded defaults none of them shadows),
	// while the CONSISTENCY check judges only what the operator authored. An
	// on-disk wildcard workflow legitimately overrides the embedded wildcard
	// baseline, so feeding both to the ambiguity check would report every repo as
	// misconfigured for doing the normal thing.
	roots := o.authoredRoots(dataDir)
	wfDir := roots.WorkflowsDir()
	authored := WorkflowDocsIn(roots)
	governing := governingFrom(authored)
	skillBody := SkillBodyResolver(dataDir)
	resolve := func(skill string) bool { _, ok := skillBody(skill); return ok }
	vars := RepoVars(dataDir)

	// Skill BODIES ride along: a check that judges a deployed repo can see whether
	// a reviewer's rubric shells `satelle`, and so whether a shell grant is live
	// rather than idle (sty_338a53f8).
	av := agentvalidate.ValidateEffectiveLayered(layers, global, vars, governing, skillBody)
	rep.Findings = append(rep.Findings, av.Findings...)
	rep.Grants = av.Grants
	rep.Findings = append(rep.Findings, BindingWiring(o.RepoRoot, o.wiringConfig(dataDir), av.Grants)...)
	rep.Gates = av.Gates
	rep.Sources = av.Provenance
	rep.Env = envKeys(layers.Authored(), config.LayerVars(global.Vars, vars))

	// 2. Authored substrate contracts, store-free (doctor runs before indexing).
	for _, kind := range []string{"workflows", "skills", "principles", "tasks"} {
		dir := filepath.Join(dataDir, kind)
		if kind == "workflows" {
			dir = wfDir
		}
		rep.Findings = append(rep.Findings, checkAuthoredDir(kind, dir, resolve)...)
	}

	// 2b. The process of record (sty_d6e209aa). An UNREADABLE workflows dir was
	// reported above, as an error. An ABSENT one keeps the embedded backstop and
	// is reported here as a warning, so the gates are never silently the
	// binary's; and a linked worktree whose own copy of the authored process
	// differs from the main tree's is listed file by file.
	if state, _ := docindex.ProbeDir(wfDir); state == docindex.DirAbsent {
		rep.Findings = append(rep.Findings, health.Warn(health.IDProcessAbsent, "Authored process absent",
			wfgovern.AbsentMessage(wfDir)).
			About(wfDir).
			WithRemediation("run `satelle init` to seed the workflows dir, or author done.toml + step.toml under it"))
	}
	if o.Process != nil && strings.TrimSpace(o.RepoRoot) != "" && strings.TrimSpace(o.ProcessRoot) != "" {
		if notice := config.DivergenceNotice(o.RepoRoot, o.ProcessRoot,
			config.ProcessDivergence(o.RepoRoot, o.ProcessRoot, *o.Process)); notice != "" {
			rep.Findings = append(rep.Findings, health.Warn(health.IDProcessDivergent, "Worktree process diverges", notice).
				About(o.RepoRoot).
				WithRemediation("make the change in the main tree (it governs), or discard the worktree's copy"))
		}
	}
	rep.Findings = dedupeFindings(rep.Findings)

	// 3. Cross-workflow consistency — ambiguity, unresolved gate and HOOK skills.
	for _, p := range agentstep.WorkflowConsistency(authored, resolve) {
		rep.Findings = append(rep.Findings, health.Error(health.IDWorkflowConsistency,
			"Workflow consistency", p).
			WithRemediation("fix the workflow's applies_to or the skill it names"))
	}

	// 4. Required binaries: every isolated binding's executable must be runnable.
	rep.Findings = append(rep.Findings, checkBinaries(av.Grants)...)

	// 4b. Reviewer tool isolation (sty_ef3efb51): a reviewer binding dispatch would
	// refuse is surfaced before an upgrade turns it into a refused gate.
	rep.Findings = append(rep.Findings, checkReviewerIsolation(av.Grants)...)

	// 5b. Leftover machine-scope keys in repo files (config.MachineScopeStrays
	// owns the rule; doctor only surfaces it — sty_21a7d16d AC4).
	for _, s := range config.MachineScopeStrays(dataDir) {
		rep.Findings = append(rep.Findings, health.Warn(health.IDConfigStray, "Stray machine-scope key",
			s.Warning()).About(s.File).WithRemediation("run `satelle migrate --yes`"))
	}

	// 5b'. The substrate lock opt-out (sty_992cffc6): reported only when the
	// operator wrote lock_substrate_paths = [] — an absent key is the default
	// lock and says nothing.
	rep.Findings = append(rep.Findings, checkSubstrateLock(dataDir)...)

	// 5c. Recorded workstate-push health (sty_30696eeb). Reads syncstate only.
	globalDir := strings.TrimSpace(o.GlobalDir)
	if globalDir == "" {
		globalDir = config.GlobalDir()
	}
	rep.Findings = append(rep.Findings, checkSyncHealth(o.RepoRoot, dataDir, globalDir, time.Now())...)
	rep.OK = rep.Findings.OK()
	return rep
}

// checkBinaries reports whether each isolated binding's executable can be found.
// An in-loop binding runs no subprocess, so it is exempt. Command TEMPLATES are
// parsed to argv[0] only — no expansion, no execution.
func checkBinaries(grants []agentvalidate.Grant) health.Findings {
	var out health.Findings
	seen := map[string]bool{}
	for _, g := range grants {
		if g.Backend == "in-loop" || g.Backend == "invalid" {
			continue
		}
		fields := strings.Fields(g.Command)
		if len(fields) == 0 {
			out = append(out, health.Error(health.IDBinaryMalformed, "Malformed command",
				fmt.Sprintf("agents.toml [%s] command is empty — no executable to run", g.Name)).
				About(g.Name).WithRemediation("set a full command template on ["+g.Name+"]"))
			continue
		}
		bin := fields[0]
		if strings.ContainsAny(bin, "{}$") {
			out = append(out, health.Error(health.IDBinaryMalformed, "Malformed command",
				fmt.Sprintf("agents.toml [%s] command starts with %q — the first token must be the executable, not a placeholder", g.Name, bin)).
				About(g.Name).WithRemediation("put the binary first in the ["+g.Name+"] command template"))
			continue
		}
		if seen[bin] {
			continue
		}
		seen[bin] = true
		if _, err := exec.LookPath(bin); err != nil {
			// ADVISORY, not an error. A repo is legitimately initialised before its
			// agent CLI is installed — satelle's gates stay inert until one is, and
			// CI initialises repos with no provider CLI at all. Making this blocking
			// would refuse `satelle init` and every engagement on such a machine for
			// a condition the operator can fix later, and which dispatch already
			// refuses loudly at the moment it actually matters. A MALFORMED command
			// below stays an error: no environment can make it work.
			out = append(out, health.Warn(health.IDBinaryMissing, "Missing executable",
				fmt.Sprintf("agents.toml [%s] needs %q, which is not on PATH", g.Name, bin)).
				About(g.Name).WithRemediation("install "+bin+", or point ["+g.Name+"] at a CLI you have"))
		}
	}
	return out
}

// checkReviewerIsolation reports once per role=reviewer binding whose command
// the dispatch preflight (agentcli.PreflightReviewer) cannot hold to its
// read-only grant, or whose grant names a write-capable tool. Such a binding is
// REFUSED at dispatch, so it is an error here.
func checkReviewerIsolation(grants []agentvalidate.Grant) health.Findings {
	var out health.Findings
	for _, g := range grants {
		if g.Role != config.RoleReviewer || g.Backend == "in-loop" || g.Backend == "invalid" {
			continue
		}
		if bad := agentcli.WriteCapableGrant(g.Tools); len(bad) > 0 {
			out = append(out, health.Error(health.IDReviewerIsolation, "Reviewer tool isolation",
				fmt.Sprintf("reviewer binding %q grants write-capable tools %s; it is refused at every dispatch", g.Name, strings.Join(bad, ", "))).
				About(g.Name).WithRemediation("for ["+g.Name+"]: grant read-only tools only"))
			continue
		}
		gaps := agentcli.PreflightReviewer(g.Interface, g.Command, g.Tools)
		if len(gaps) == 0 {
			continue
		}
		out = append(out, health.Error(health.IDReviewerIsolation, "Reviewer tool isolation",
			fmt.Sprintf("reviewer binding %q (%s) is refused at every dispatch: %s", g.Name, gaps[0].Adapter, agentcli.GapSummary(gaps))).
			About(g.Name).WithRemediation("for ["+g.Name+"]: "+agentcli.GapFix(gaps)))
	}
	return out
}

// checkSubstrateLock warns when the committed config carries an explicit empty
// [gate] lock_substrate_paths. It keys off config.ParseLockSubstratePaths'
// explicit-opt-out result, never off an empty list: an absent key and a
// present-but-empty one decode alike, and only the second is an opt-out.
func checkSubstrateLock(dataDir string) health.Findings {
	raw, _ := os.ReadFile(filepath.Join(dataDir, config.ConfigName))
	if _, optOut := config.ParseLockSubstratePaths(string(raw)); !optOut {
		return nil
	}
	return health.Findings{health.Warn(health.IDSubstrateUnlocked, "Substrate lock disabled",
		fmt.Sprintf("%s sets [gate] lock_substrate_paths = [] — a story that holds a performing seat can rewrite the workflows, skills and agent bindings that are about to judge it, and the change leaves no trace in its engagement diff",
			config.DefaultDataDir+"/"+config.ConfigName)).
		About(config.DefaultDataDir + "/" + config.ConfigName).
		WithRemediation(`delete lock_substrate_paths (the default locks ".satelle/") or list the prefixes to lock`)}
}

// unreadableProcessFinding is the one finding for an unreadable authored workflows
// dir. agentvalidate builds the same finding for `satelle agent validate`; doctor
// runs both, so Check de-duplicates (dedupeFindings).
func unreadableProcessFinding(dir string, err error) health.Finding {
	return health.Error(health.IDProcessUnreadable, "Authored process unreadable",
		wfgovern.UnreadableMessage(dir, err.Error())).
		About(dir).WithRemediation("restore the workflows dir (it must be a readable directory) before engaging a story")
}

// dedupeFindings drops a finding identical (id, severity, detail) to an earlier
// one, keeping the first. Two checks can legitimately see the same defect.
func dedupeFindings(in health.Findings) health.Findings {
	seen := map[string]bool{}
	out := make(health.Findings, 0, len(in))
	for _, f := range in {
		k := f.ID + "\x00" + string(f.Severity) + "\x00" + f.Detail
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// checkAuthoredDir applies each authored kind's structure contract, returning
// findings instead of printing. It mirrors the loop `satelle validate` runs, so
// init and doctor judge the same files by the same rules.
func checkAuthoredDir(kind, dir string, resolve func(string) bool) health.Findings {
	var out health.Findings
	state, perr := docindex.ProbeDir(dir)
	if state == docindex.DirAbsent {
		return nil // an absent kind is not a defect; init seeds what it needs
	}
	if state == docindex.DirUnreadable {
		// An authored dir that exists and cannot be read is a defect, not an absent
		// kind. For workflows it is the named process-unreadable finding the other
		// surfaces print (sty_d6e209aa).
		if kind == "workflows" {
			return health.Findings{unreadableProcessFinding(dir, perr)}
		}
		return health.Findings{health.Error(health.IDWorkflowStructure, "Unreadable substrate",
			fmt.Sprintf("%s — %s", kind, perr)).About(kind)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		fn := e.Name()
		if e.IsDir() || !docindex.Indexable(fn) {
			continue
		}
		if kind == "tasks" && !strings.HasPrefix(fn, "tsk_") {
			continue
		}
		name := strings.TrimSuffix(fn, filepath.Ext(fn))
		if reservedKeepFile(fn) {
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(dir, fn))
		if rerr != nil {
			out = append(out, health.Error(health.IDWorkflowStructure, "Unreadable substrate",
				fmt.Sprintf("%s/%s — read: %v", kind, name, rerr)).About(kind+"/"+name))
			continue
		}
		var problems []string
		switch kind {
		case "documents":
			if derr := docindex.OKFConformance(name, string(body)); derr != nil {
				problems = []string{derr.Error()}
			}
		case "tasks":
			problems = structure.CheckTask(string(body))
		default:
			problems = structure.Doc(kind, name, string(body), resolve)
		}
		for _, p := range problems {
			out = append(out, health.Error(health.IDWorkflowStructure, "Substrate structure",
				fmt.Sprintf("%s/%s — %s", kind, name, p)).
				About(kind+"/"+name).WithRemediation("fix the authored file under .satelle/"+kind))
		}
	}
	return out
}

// envKeys lists each binding's environment KEY NAMES with whether the value
// resolved. Values never appear — an env value may be a credential, and this
// payload is printed and pasted.
func envKeys(agents config.AgentsConfig, vars map[string]string) map[string][]EnvKey {
	out := map[string][]EnvKey{}
	add := func(section string, b config.AgentBinding) {
		if len(b.Env) == 0 {
			return
		}
		keys := make([]EnvKey, 0, len(b.Env))
		for _, k := range sortedKeys(b.Env) {
			_, err := config.ExpandVars(b.Env[k], vars)
			keys = append(keys, EnvKey{Key: k, Resolved: err == nil})
		}
		out[section] = keys
	}
	add("executor", agents.Executor)
	add("reviewer", agents.Reviewer)
	for _, name := range sortedBindingNames(agents.Agents) {
		add(name, agents.Agents[name])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WorkflowDocs reads deployed workflow files as docs — store-free, because
// doctor must work on a repo that has never been indexed. Exported so init and
// its tests read the deployed set through the SAME function doctor judges, not a
// second copy that could drift.
//
// An UNREADABLE dir (it exists and cannot be listed, or is not a directory) is
// not an absent one: the answer is the same unreadable sentinel the store-backed
// loader returns for it, decided by the same docindex.ProbeDir, so no consumer
// reads it as "no workflows" and falls back to the embedded route
// (sty_d6e209aa). An absent dir stays nil.
func WorkflowDocs(dataDir string) []docindex.Doc {
	return WorkflowDocsIn(config.AuthoredRootsOf(dataDir))
}

// WorkflowDocsIn is WorkflowDocs over authored roots: with a project tier, the
// tier's workflows replace the repo's for every path the team owns.
func WorkflowDocsIn(r config.AuthoredRoots) []docindex.Doc {
	repo := WorkflowDocsAt(r.Dirs["workflows"])
	if strings.TrimSpace(r.Tier) == "" {
		return repo
	}
	tier := WorkflowDocsAt(r.Index().TierDir("workflows"))
	for _, set := range [][]docindex.Doc{repo, tier} {
		if _, _, unreadable := docindex.UnreadableOf(set); unreadable {
			return set
		}
	}
	var out []docindex.Doc
	for _, d := range repo {
		if !r.Owns("workflows/" + filepath.Base(d.Path)) {
			out = append(out, d)
		}
	}
	for _, d := range tier {
		if r.Owns("workflows/" + filepath.Base(d.Path)) {
			out = append(out, d)
		}
	}
	return out
}

// WorkflowDocsAt is WorkflowDocs for an explicit workflows dir — the one the
// process config resolves, which a [substrate_roots] override moves off
// <data dir>/workflows (sty_d6e209aa).
func WorkflowDocsAt(dir string) []docindex.Doc {
	state, perr := docindex.ProbeDir(dir)
	if state == docindex.DirUnreadable {
		return []docindex.Doc{docindex.UnreadableDoc("workflows", dir, perr)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var docs []docindex.Doc
	for _, e := range entries {
		fn := e.Name()
		// The route source is TOML (sty_81bb0dde). A `.md`-only filter here does
		// not merely miss it — GoverningWorkflows appends an embedded default for
		// any name it does not see on disk, so the repo's OWN route was silently
		// replaced by the shipped one, and every binding the authored route
		// allocates was then reported orphaned by `satelle agent validate`.
		if e.IsDir() || !docindex.Indexable(fn) || reservedKeepFile(fn) {
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(dir, fn))
		if rerr != nil {
			// A file that exists and cannot be read is the authored process
			// unreadable one level down — skipping it would let GoverningWorkflows
			// substitute the embedded half of that name (sty_d6e209aa).
			return []docindex.Doc{docindex.UnreadableDoc("workflows", filepath.Join(dir, fn), rerr)}
		}
		docs = append(docs, docindex.Doc{
			Kind: "workflows",
			Name: strings.TrimSuffix(fn, filepath.Ext(fn)),
			Path: filepath.Join(dir, fn),
			Ext:  strings.ToLower(filepath.Ext(fn)),
			Body: string(body),
		})
	}
	return docs
}

// GoverningWorkflows is the set of workflows that actually GOVERN this repo: the
// authored files on disk, plus the binary's embedded defaults that no on-disk
// file shadows by name.
//
// Reading only the authored dir would leave a repo governed by an embedded
// default with no allocation or lifecycle-hook checking at all — doctor would
// report it healthy while never having looked at the workflow that runs. The
// same union is what the resolution surfaces see once the substrate is indexed;
// doctor computes it store-free so it also works before the first reindex.
func GoverningWorkflows(dataDir string) []docindex.Doc {
	return governingFrom(WorkflowDocs(dataDir))
}

// GoverningWorkflowsAt is GoverningWorkflows for an explicit workflows dir; see
// WorkflowDocsAt.
func GoverningWorkflowsAt(dir string) []docindex.Doc {
	return governingFrom(WorkflowDocsAt(dir))
}

// governingFrom is the authored docs plus the embedded workflows none shadows.
func governingFrom(docs []docindex.Doc) []docindex.Doc {
	// An unreadable authored dir governs nothing: overlaying the embedded
	// workflows here would make every consumer judge a route that is not the
	// repository's as if it were (sty_d6e209aa).
	if _, _, unreadable := docindex.UnreadableOf(docs); unreadable {
		return docs
	}
	onDisk := map[string]bool{}
	for _, d := range docs {
		onDisk[d.Name] = true
	}
	for _, e := range config.EmbeddedDefaults() {
		if e.Kind != "workflows" || onDisk[e.Name] {
			continue
		}
		// Embedded/Path are set so downstream can tell a SHIPPED default from an
		// authored file — RouteGoverns keys order-zero precedence on it, and the
		// legacy-markdown refusal must never fire on the binary's own defaults.
		docs = append(docs, docindex.Doc{
			Kind: "workflows", Name: e.Name, Body: e.Body,
			Ext:      defaultExt(e.Ext),
			Path:     "embedded:workflows/" + e.Name + defaultExt(e.Ext),
			Embedded: true,
		})
	}
	return docs
}

// SkillBodyResolver reads a skill's body by name: the authored file under
// dataDir when present, else the embedded default it would resolve to (the
// virtual sparse defaults rule). It is the SINGLE disk-else-embedded lookup —
// SkillResolver is derived from it, so "does this skill resolve" and "what does
// it say" can never answer from different sources (sty_338a53f8).
func SkillBodyResolver(dataDir string) func(string) (string, bool) {
	return SkillBodyResolverIn(config.AuthoredRootsOf(dataDir))
}

// SkillBodyResolverIn is SkillBodyResolver over authored roots: a skill the
// team owns reads from the project tier, never from the repo's copy.
func SkillBodyResolverIn(r config.AuthoredRoots) func(string) (string, bool) {
	emb := map[string]string{}
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "skills" {
			emb[d.Name] = d.Body
		}
	}
	return func(skill string) (string, bool) {
		path := filepath.Join(r.Dirs["skills"], skill+".md")
		if rel := "skills/" + skill + ".md"; r.Owns(rel) {
			path = filepath.Join(r.Tier, filepath.FromSlash(rel))
		}
		if b, err := os.ReadFile(path); err == nil {
			return string(b), true
		}
		body, ok := emb[skill]
		return body, ok
	}
}

// SkillResolver reports whether a skill resolves on disk or as an embedded
// default (the virtual sparse defaults rule). Exported for the same reason as
// WorkflowDocs.
func SkillResolver(dataDir string) func(string) bool {
	body := SkillBodyResolver(dataDir)
	return func(skill string) bool {
		_, ok := body(skill)
		return ok
	}
}

// RepoVars loads the repo's [vars] KV (with its gitignored overlay).
func RepoVars(dataDir string) map[string]string {
	cfg, _, err := config.Load(filepath.Join(dataDir, config.ConfigName))
	if err != nil {
		return nil
	}
	return cfg.Vars
}

// reservedKeepFile mirrors the authored-dir convention: a README is a keep-file,
// not substrate to validate.
func reservedKeepFile(fn string) bool {
	switch strings.ToLower(fn) {
	case "readme.md", ".gitkeep", ".keep":
		return true
	}
	return false
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortedBindingNames(m map[string]config.AgentBinding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// defaultExt is an embedded default's extension, defaulting to markdown for a
// default authored before Ext existed (sty_81bb0dde).
func defaultExt(ext string) string {
	if strings.TrimSpace(ext) == "" {
		return ".md"
	}
	return ext
}
