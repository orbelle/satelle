// resolveagents.go — the deterministic precedence engine that folds the
// machine-wide profile catalog into a repo's agents layer (sty_c7dfeedf).
//
// The ladder, highest first:
//
//  0. local   — an inline value in .satelle/workflows/agents.local.toml, this
//     machine's own override, which never syncs (sty_5c56143b); it may also name
//     the profile=, taking precedence over the repo's; between it and the repo
//     sits global-seat, the profile the catalog's [seats] map names for this
//     seat on every repo of the machine, which a local profile= suppresses
//  1. repo    — an inline value on the binding in .satelle/workflows/agents.toml
//  2. profile — the catalog profile the binding EXPLICITLY names via profile=
//     (and, transitively, whatever that profile extends)
//  3. global-role — the catalog's [roles] default for the binding's role, and
//     ONLY when the repo opted in with [defaults] use_global_roles
//  4. embedded — the binary's safe fallback (DefaultExecutorCommand,
//     DefaultReviewerCommand, DefaultReviewerTools), applied as today
//     by the *Binding resolvers
//
// The critical negative: tier 2 and tier 3 are reached only by an explicit
// reference. A catalog profile named "reviewer" and a repo [reviewer] that never
// mentions it DO NOT merge — the repo resolves byte-identically with and without
// the catalog present. Every convenience that would blur this is deliberately
// absent, because a machine-wide file that can silently re-point a pinned repo's
// reviewer is exactly the failure this design exists to prevent.
//
// Resolution returns the same AgentsConfig shape everything downstream already
// consumes, plus a Provenance side-table, so no dispatch or gate code changes.
package config

import (
	"fmt"
	"sort"
	"strings"
)

// Source labels recorded in Provenance. Profile and role sources carry the
// profile name, so a display surface names WHICH profile a field came from.
const (
	SourceRepo     = "repo"
	SourceEmbedded = "embedded"
	// SourceWorkspace labels a field supplied by the synced workspace bindings
	// layer (agents.workspace.toml, sty_01949949) — the tier directly under the
	// repo's own file, above any catalog profile.
	SourceWorkspace = "workspace"
	// SourceBaseline labels a field supplied by the embedded baseline seat list
	// (BaselineAgents) — the lowest overlay, under the workspace and repo tiers.
	SourceBaseline = "baseline"
	// SourceLocal labels a field supplied by the machine-local override
	// (agents.local.toml, sty_5c56143b) — the top tier, above the repo's file.
	SourceLocal = "local"
)

// SourceFile names the file (or origin) behind a Provenance label, so a display
// can say WHERE a field came from and not only which tier. A key-wise merged
// map field carries a composite label ("local+repo"); each part is mapped and
// the distinct files are joined. An unknown label is returned unchanged.
func SourceFile(label string) string {
	var files []string
	for _, part := range strings.Split(label, "+") {
		f := sourceFile(part)
		dup := false
		for _, e := range files {
			dup = dup || e == f
		}
		if !dup {
			files = append(files, f)
		}
	}
	return strings.Join(files, " + ")
}

func sourceFile(label string) string {
	switch {
	case label == SourceLocal:
		return DefaultDataDir + "/" + LocalAgentsRel
	case label == SourceRepo:
		return DefaultDataDir + "/" + AgentsRel
	case label == SourceWorkspace:
		return DefaultDataDir + "/" + WorkspaceAgentsRel
	case IsCatalogSource(label):
		return GlobalAgentsLabel
	case label == SourceBaseline, label == SourceEmbedded:
		return SourceEmbedded
	}
	return label
}

// SourceProfile labels a field won by an explicitly referenced catalog profile.
func SourceProfile(profile string) string { return "profile:" + profile }

// SourceGlobalRole labels a field won by the catalog's opt-in [roles] default.
func SourceGlobalRole(profile string) string { return "global-role:" + profile }

// SourceGlobalSeat labels a field won by the catalog's [seats] map, which names
// the seat's profile for every repo on this machine.
func SourceGlobalSeat(profile string) string { return "global-seat:" + profile }

// catalogSourcePrefixes are the label prefixes of the three tiers that reach a
// catalog profile, so every reader of a label recognises them the same way.
var catalogSourcePrefixes = []string{"profile:", "global-role:", "global-seat:"}

// CatalogProfile returns the catalog profile named by one label part
// ("profile:p", "global-role:p" or "global-seat:p"), and whether it is one.
func CatalogProfile(part string) (string, bool) {
	for _, p := range catalogSourcePrefixes {
		if name, ok := strings.CutPrefix(part, p); ok {
			return name, true
		}
	}
	return "", false
}

// IsCatalogSource reports whether any part of a label (a key-wise merged map
// field carries "a+b") came from the machine catalog.
func IsCatalogSource(label string) bool {
	for _, part := range strings.Split(label, "+") {
		if _, ok := CatalogProfile(part); ok {
			return true
		}
	}
	return false
}

// Provenance records, per binding section, the source of every effective field.
// Keys are the TOML field names (command, tools, model, …) so a display surface
// can render "field value (source)" without re-deriving anything.
type Provenance map[string]map[string]string

// Source returns the recorded source for one field, or "" when the field was
// never set by any tier (no value, so nothing to attribute).
func (p Provenance) Source(section, field string) string {
	if p == nil {
		return ""
	}
	return p[section][field]
}

// Fields returns the sourced field names for a section, sorted — a stable
// iteration order for rendering and snapshot tests.
func (p Provenance) Fields(section string) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p[section]))
	for k := range p[section] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EffectiveAgents is the resolved agents layer plus what a caller needs to
// explain and expand it: where each field came from, and the layered ${VAR} KV.
type EffectiveAgents struct {
	// Agents is the resolved layer — the same shape LoadAgents returns, so every
	// downstream consumer (dispatch, gates, validate) is unchanged.
	Agents AgentsConfig
	// Provenance is the per-field source table for display and validation.
	Provenance Provenance
	// Vars is the catalog's [vars] layered UNDER the repo's, repo keys winning.
	// Expansion still happens in memory at wiring time (ResolveAgentEnvs) — a
	// catalog secret is never written into a repository.
	Vars map[string]string
}

// LoadEffectiveAgents is the SINGLE resolution site: read the repo layer, read
// the machine-wide catalog, and fold them by the documented precedence. Every
// surface that needs the effective agents layer calls this rather than merging
// on its own — two independently-merging call sites is the defect this exists to
// prevent. repoVars is the repo's [vars] KV (may be nil).
func LoadEffectiveAgents(dataDir string, repoVars map[string]string) (EffectiveAgents, error) {
	layers, err := LoadAgentLayers(dataDir)
	if err != nil {
		return EffectiveAgents{}, err
	}
	global, err := LoadGlobalAgents()
	if err != nil {
		return EffectiveAgents{}, err
	}
	baseline, err := BaselineAgents()
	if err != nil {
		return EffectiveAgents{}, err
	}
	agents, prov, err := ResolveAgentsBaseline(baseline, layers, global)
	if err != nil {
		return EffectiveAgents{}, err
	}
	return EffectiveAgents{Agents: agents, Provenance: prov, Vars: LayerVars(global.Vars, repoVars)}, nil
}

// ResolveEffectiveAgents folds an already-loaded repo layer and catalog with no
// workspace layer. Split from LoadEffectiveAgents so tests and multi-repo
// fixtures can resolve without disk, and so the precedence logic has no I/O in it.
func ResolveEffectiveAgents(repo AgentsConfig, global GlobalAgentsConfig, repoVars map[string]string) (EffectiveAgents, error) {
	return ResolveEffectiveAgentsLayered(repo, AgentsConfig{}, global, repoVars)
}

// ResolveEffectiveAgentsLayered is ResolveEffectiveAgents with the synced
// workspace bindings layer in the ladder (sty_01949949). A zero workspace
// resolves byte-identically to ResolveEffectiveAgents.
func ResolveEffectiveAgentsLayered(repo, workspace AgentsConfig, global GlobalAgentsConfig, repoVars map[string]string) (EffectiveAgents, error) {
	agents, prov, err := ResolveAgentsLayered(repo, workspace, global)
	if err != nil {
		return EffectiveAgents{}, err
	}
	return EffectiveAgents{Agents: agents, Provenance: prov, Vars: LayerVars(global.Vars, repoVars)}, nil
}

// LayerVars returns the ${NAME} KV with the machine-wide catalog as the base and
// the repo's own [vars] (including its gitignored satelle.local.toml overlay)
// winning per key — the same per-key overlay shape the repo already uses for its
// own layers. Neither map is mutated.
func LayerVars(global, repo map[string]string) map[string]string {
	if len(global) == 0 && len(repo) == 0 {
		return nil
	}
	out := make(map[string]string, len(global)+len(repo))
	for k, v := range global {
		out[k] = v
	}
	for k, v := range repo {
		out[k] = v
	}
	return out
}

// ResolveAgents folds the catalog into the repo layer and returns the effective
// agents config with its per-field provenance. A missing referenced profile, a
// reference cycle, or a repo/profile role conflict is an error — resolution
// never half-applies a broken reference.
func ResolveAgents(repo AgentsConfig, global GlobalAgentsConfig) (AgentsConfig, Provenance, error) {
	return ResolveAgentsLayered(repo, AgentsConfig{}, global)
}

// ResolveAgentsLayered is the ladder with the synced workspace bindings layer
// in it (sty_01949949), highest first: repo → workspace → profile /
// global-role → embedded. The workspace tier sits directly under the repo's
// file: a repo table wins field by field (overlayBinding skips blanks), a
// workspace-only field fills a blank, and a workspace-only SECTION applies
// whole — so the iteration is the sorted UNION of both layers' named bindings,
// not the repo's alone. Defaults stay the repo's: the workspace does not opt a
// repo into global roles or a secondary it did not write.
func ResolveAgentsLayered(repo, workspace AgentsConfig, global GlobalAgentsConfig) (AgentsConfig, Provenance, error) {
	return ResolveAgentsBaseline(AgentsConfig{}, AgentLayers{Repo: repo, Workspace: workspace}, global)
}

// ResolveAgentsBaseline is ResolveAgentsLayered with the embedded baseline seat
// list beneath it (sty_6602bb44). The seat names are the sorted UNION of the
// baseline, repo and workspace; per seat the baseline is the lowest overlay
// (above only the profile chain), so a repo table overrides just the fields it
// writes and a seat the repo does not name keeps the baseline value. The
// catalog's [roles] default reaches a seat only when the repo set
// use_global_roles, whether or not the repo named that seat. A zero baseline
// resolves byte-identically to ResolveAgentsLayered.
//
// The machine-local layer (agents.local.toml, sty_5c56143b) is the top tier: it
// overlays the repo's file field by field, may name the profile= (taking
// precedence over the repo's), and its [defaults] keys override the repo's key
// by key. An empty local layer resolves byte-identically to before.
func ResolveAgentsBaseline(baseline AgentsConfig, layers AgentLayers, global GlobalAgentsConfig) (AgentsConfig, Provenance, error) {
	repo, workspace, local := layers.Repo, layers.Workspace, layers.Local
	out := AgentsConfig{Defaults: overlayDefaults(repo.Defaults, local.Defaults), Agents: map[string]AgentBinding{}}
	prov := Provenance{}
	useRoles := out.Defaults.UseGlobalRoles

	resolve := func(section string, baseB, repoB, wsB, localB AgentBinding) (AgentBinding, error) {
		merged, src, err := resolveBindingProfile(section, baseB, repoB, wsB, localB, global, useRoles)
		if err != nil {
			return AgentBinding{}, err
		}
		// The embedded tier of the ladder: a seat the shipped route allocates and
		// no tier gave a grant still gets the read-only one, so a repo that runs the
		// shipped route unconfigured can dispatch it (sty_25064a6c).
		if grant, ok := embeddedSeatTools[section]; ok && strings.TrimSpace(merged.Tools) == "" {
			merged.Tools = grant
			src["tools"] = SourceEmbedded
		}
		markEmbedded(section, merged, src)
		prov[section] = src
		return merged, nil
	}

	var err error
	if out.Executor, err = resolve("executor", baseline.Executor, repo.Executor, workspace.Executor, local.Executor); err != nil {
		return AgentsConfig{}, nil, err
	}
	if out.Reviewer, err = resolve("reviewer", baseline.Reviewer, repo.Reviewer, workspace.Reviewer, local.Reviewer); err != nil {
		return AgentsConfig{}, nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, layer := range []map[string]AgentBinding{baseline.Agents, repo.Agents, workspace.Agents, local.Agents} {
		for n := range layer {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, rerr := resolve(name, baseline.Agents[name], repo.Agents[name], workspace.Agents[name], local.Agents[name])
		if rerr != nil {
			return AgentsConfig{}, nil, rerr
		}
		out.Agents[name] = b
	}
	out.ModelOrder = layerModelOrder(baseline.ModelOrder, workspace.ModelOrder, repo.ModelOrder, local.ModelOrder)
	return out, prov, nil
}

// overlayDefaults lays the machine-local [defaults] over the repo's key by key:
// a key the local file leaves unset keeps the repo's value. use_global_roles is
// a bool, so the local file can switch it ON but cannot switch a shared true off
// (the shared file is warned off carrying it in the first place).
func overlayDefaults(base, top AgentsDefaults) AgentsDefaults {
	out := base
	if strings.TrimSpace(top.Secondary) != "" {
		out.Secondary = top.Secondary
	}
	if top.UseGlobalRoles {
		out.UseGlobalRoles = true
	}
	if len(top.LiveInterfaceOrder) > 0 {
		out.LiveInterfaceOrder = top.LiveInterfaceOrder
	}
	if strings.TrimSpace(top.IdleTimeout) != "" {
		out.IdleTimeout = top.IdleTimeout
	}
	if strings.TrimSpace(top.BusyTimeout) != "" {
		out.BusyTimeout = top.BusyTimeout
	}
	if top.ContextBudget != 0 {
		out.ContextBudget = top.ContextBudget
	}
	if top.TurnBudget != 0 {
		out.TurnBudget = top.TurnBudget
	}
	return out
}

// layerModelOrder folds the [model_order] layers per executable key: the
// highest layer that names an executable wins that executable's list, so a repo
// writing only claude still inherits the workspace's or baseline's grok
// list. Layers are passed lowest first.
func layerModelOrder(layers ...map[string][]ModelRank) map[string][]ModelRank {
	var out map[string][]ModelRank
	for _, l := range layers {
		for exe, ranks := range l {
			if len(ranks) == 0 {
				continue
			}
			if out == nil {
				out = map[string][]ModelRank{}
			}
			out[strings.ToLower(strings.TrimSpace(exe))] = ranks
		}
	}
	return out
}

// resolveBindingProfile merges one binding against the workspace layer and the
// catalog, returning the effective binding and its per-field sources. Order:
// the profile chain (reached only by an explicit reference or opt-in) is the
// base; the baseline, workspace and repo overlay it in turn; the catalog's
// [seats] profile for this seat overlays the repo; the local file overlays last.
func resolveBindingProfile(section string, baseB, repoB, wsB, localB AgentBinding, global GlobalAgentsConfig, useRoles bool) (AgentBinding, map[string]string, error) {
	base := AgentBinding{}
	baseSrc := map[string]string{}

	// profile= on the machine-local seat wins, then the repo seat; a baseline
	// seat may name one too. [roles] is reached only when use_global_roles was
	// set. A seat nobody named does not get its own path to the catalog: a
	// machine [roles] default must not retarget a repo that never asked.
	profileRef, profileFile := localB.Profile, LocalAgentsName
	if strings.TrimSpace(profileRef) == "" {
		profileRef, profileFile = repoB.Profile, AgentsConfigName
	}
	if strings.TrimSpace(profileRef) == "" {
		profileRef = baseB.Profile
	}

	if ref := strings.TrimSpace(profileRef); ref != "" {
		chain, err := global.resolveChain(ref)
		if err != nil {
			return AgentBinding{}, nil, fmt.Errorf("%s [%s] profile: %w", profileFile, section, err)
		}
		base, baseSrc = foldChain(global, chain, SourceProfile)
	} else if useRoles {
		// Tier 3 — reached only because the repo wrote use_global_roles. Absent
		// that opt-in there is no path from the catalog to a binding that did not
		// name a profile, which is what keeps a same-name profile inert.
		roleB := repoB
		if strings.TrimSpace(roleB.Role) == "" {
			roleB.Role = localB.Role
		}
		if strings.TrimSpace(roleB.Role) == "" {
			roleB.Role = baseB.Role
		}
		role := ResolvedRole(section, roleB)
		if target := strings.TrimSpace(global.Roles[role]); target != "" {
			chain, err := global.resolveChain(target)
			if err != nil {
				return AgentBinding{}, nil, fmt.Errorf("%s [defaults] use_global_roles: [roles] %s: %w", AgentsConfigName, role, err)
			}
			base, baseSrc = foldChain(global, chain, SourceGlobalRole)
		}
	}

	// Role is IDENTITY, not an overridable field: a repo that declares a role
	// differing from the profile's is stating two incompatible contracts, so it
	// is refused rather than silently resolved in either direction. Restating the
	// same role is fine and common.
	repoRole := strings.ToLower(strings.TrimSpace(repoB.Role))
	baseRole := strings.ToLower(strings.TrimSpace(base.Role))
	if repoRole != "" && baseRole != "" && repoRole != baseRole {
		return AgentBinding{}, nil, fmt.Errorf(
			"%s [%s] declares role=%q but %s declares role=%q for the profile it references — role is identity and must agree",
			AgentsConfigName, section, repoB.Role, GlobalAgentsLabel, base.Role)
	}
	// The workspace layer is held to the same identity rule against the repo.
	wsRole := strings.ToLower(strings.TrimSpace(wsB.Role))
	if repoRole != "" && wsRole != "" && repoRole != wsRole {
		return AgentBinding{}, nil, fmt.Errorf(
			"%s [%s] declares role=%q but the workspace layer (%s) declares role=%q — role is identity and must agree",
			AgentsConfigName, section, repoB.Role, WorkspaceAgentsRel, wsB.Role)
	}

	// The baseline is held to the same identity rule against the repo.
	baselineRole := strings.ToLower(strings.TrimSpace(baseB.Role))
	if repoRole != "" && baselineRole != "" && repoRole != baselineRole {
		return AgentBinding{}, nil, fmt.Errorf(
			"%s [%s] declares role=%q but the baseline seat declares role=%q — role is identity and must agree",
			AgentsConfigName, section, repoB.Role, baseB.Role)
	}

	// The machine-local override is held to the same identity rule against every
	// shared statement of the role: the repo's binding, the baseline seat and the
	// profile it references.
	localRole := strings.ToLower(strings.TrimSpace(localB.Role))
	for _, shared := range []struct{ role, from string }{
		{repoB.Role, AgentsConfigName + " [" + section + "]"},
		{baseB.Role, "the baseline seat"},
		{base.Role, "the profile it references (" + GlobalAgentsLabel + ")"},
	} {
		if r := strings.ToLower(strings.TrimSpace(shared.role)); localRole != "" && r != "" && localRole != r {
			return AgentBinding{}, nil, fmt.Errorf(
				"%s [%s] declares role=%q but %s declares role=%q — role is identity and must agree",
				LocalAgentsName, section, localB.Role, shared.from, shared.role)
		}
	}

	// The catalog's [seats] map names this seat's profile for every repo on the
	// machine: the operator naming the seat is the explicit reference. It overlays
	// the repo and sits under the local file; a local profile= is the higher
	// choice of profile, so it suppresses the seat map outright.
	seatRef := ""
	if strings.TrimSpace(localB.Profile) == "" {
		seatRef = strings.TrimSpace(global.Seats[section])
	}
	var seatChain []string
	if seatRef != "" {
		chain, err := global.resolveChain(seatRef)
		if err != nil {
			return AgentBinding{}, nil, fmt.Errorf("%s [seats] %s: %w", GlobalAgentsLabel, section, err)
		}
		seatChain = chain
		seatB, _ := foldChain(global, chain, SourceGlobalSeat)
		seatRole := strings.ToLower(strings.TrimSpace(seatB.Role))
		for _, other := range []struct{ role, from string }{
			{repoB.Role, AgentsConfigName + " [" + section + "]"},
			{wsB.Role, "the workspace layer (" + WorkspaceAgentsRel + ")"},
			{baseB.Role, "the baseline seat"},
			{base.Role, "the profile it references"},
			{localB.Role, LocalAgentsName + " [" + section + "]"},
		} {
			if r := strings.ToLower(strings.TrimSpace(other.role)); seatRole != "" && r != "" && seatRole != r {
				return AgentBinding{}, nil, fmt.Errorf(
					"%s [seats] %s = %q declares role=%q but %s declares role=%q — role is identity and must agree",
					GlobalAgentsLabel, section, seatRef, seatB.Role, other.from, other.role)
			}
		}
	}

	// Workspace under repo: a synced binding fills what the repo left blank and
	// never overrides what the repo wrote. profile= is never taken from the
	// workspace layer (a catalog name means nothing on another machine; ingest
	// refuses a layer that carries one). A blank env/settings value on this tier is
	// "declared, unsatisfied" and is DROPPED before the merge, so it can never lay
	// `KEY=` over the machine's real environment or --settings over
	// settings.local.json (AC3). A ${VAR} reference is kept and is resolved
	// locally at wiring time.
	wsB.Profile = ""
	wsB = PruneUnsatisfied(wsB)
	baseB.Profile = ""
	merged, src := overlayBinding(base, baseSrc, baseB, SourceBaseline)
	merged, src = overlayBinding(merged, src, wsB, SourceWorkspace)
	merged, src = overlayBinding(merged, src, repoB, SourceRepo)
	merged, src = foldChainOnto(merged, src, global, seatChain, SourceGlobalSeat)
	localB.Profile = ""
	merged, src = overlayBinding(merged, src, localB, SourceLocal)
	merged.Profile = profileRef
	if seatRef != "" {
		merged.Profile = seatRef
	}
	return merged, src, nil
}

// foldChain folds a profile reference chain (outermost first) into one binding,
// deepest base first so the outermost profile wins — the same direction the repo
// then wins over the whole chain. label names the tier that reached the chain.
func foldChain(global GlobalAgentsConfig, chain []string, label func(string) string) (AgentBinding, map[string]string) {
	return foldChainOnto(AgentBinding{}, map[string]string{}, global, chain, label)
}

// foldChainOnto is foldChain laid over an existing binding and its sources, so a
// chain can sit mid-ladder (the [seats] tier) with each field still naming the
// profile in the chain that supplied it. An empty chain returns b unchanged.
func foldChainOnto(b AgentBinding, src map[string]string, global GlobalAgentsConfig, chain []string, label func(string) string) (AgentBinding, map[string]string) {
	for i := len(chain) - 1; i >= 0; i-- {
		name := chain[i]
		b, src = overlayBinding(b, src, global.Profiles[name], label(name))
	}
	return b, src
}

// overlayBinding lays top over base: a non-empty scalar on top wins; env and
// settings merge key-wise with top's keys winning. src starts from baseSrc and
// records topSrc for every field top actually supplied, so the returned table
// always describes the returned binding.
func overlayBinding(base AgentBinding, baseSrc map[string]string, top AgentBinding, topSrc string) (AgentBinding, map[string]string) {
	out := base
	src := make(map[string]string, len(baseSrc)+4)
	for k, v := range baseSrc {
		src[k] = v
	}
	setScalar := func(field string, dst *string, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		*dst = val
		src[field] = topSrc
	}
	setScalar("interface", &out.Interface, top.Interface)
	setScalar("command", &out.Command, top.Command)
	setScalar("tools", &out.Tools, top.Tools)
	setScalar("model", &out.Model, top.Model)
	setScalar("role", &out.Role, top.Role)
	setScalar("principles", &out.Principles, top.Principles)
	setScalar("timeout", &out.Timeout, top.Timeout)
	setScalar("idle_timeout", &out.IdleTimeout, top.IdleTimeout)
	setScalar("busy_timeout", &out.BusyTimeout, top.BusyTimeout)
	setScalar("effort", &out.Effort, top.Effort)
	setScalar("secondary", &out.Secondary, top.Secondary)
	setScalar("isolation", &out.Isolation, top.Isolation)
	setScalar("auth", &out.Auth, top.Auth)
	setScalar("collect_doc", &out.CollectDoc, top.CollectDoc)
	setScalar("placement", &out.Placement, top.Placement)
	setScalar("remote", &out.Remote, top.Remote)
	setScalar("remote_path", &out.RemotePath, top.RemotePath)
	// Budgets are the REPO's (never a catalog profile's), so only the top tier
	// carries them; a non-zero value overlays whatever sits beneath.
	if top.ContextBudget != 0 {
		out.ContextBudget = top.ContextBudget
		src["context_budget"] = topSrc
	}
	if top.TurnBudget != 0 {
		out.TurnBudget = top.TurnBudget
		src["turn_budget"] = topSrc
	}

	if len(top.Env) > 0 {
		out.Env = mergeStringMap(base.Env, top.Env)
		src["env"] = mapSource(len(base.Env) > 0, topSrc, baseSrc["env"])
	}
	if len(top.Settings) > 0 {
		out.Settings = mergeAnyMap(base.Settings, top.Settings)
		src["settings"] = mapSource(len(base.Settings) > 0, topSrc, baseSrc["settings"])
	}
	return out, src
}

// mapSource labels a key-wise merged map field. When both tiers contributed keys
// the label names both, because attributing the whole table to the winner would
// hide that some keys came from underneath.
func mapSource(baseContributed bool, topSrc, baseSrc string) string {
	if baseContributed && baseSrc != "" && baseSrc != topSrc {
		return topSrc + "+" + baseSrc
	}
	return topSrc
}

// embeddedSeatTools is the tools grant the embedded tier gives a baseline seat
// whose resolved binding has none, beyond the [reviewer] default the *Binding
// resolvers apply. ready-reviewer is the performer of the shipped container
// readiness step; it reports changes that satelle applies, so it only reads, and
// Read is the pull-context channel a dispatched agent needs. A profile or an
// inline grant on the seat wins over this.
var embeddedSeatTools = map[string]string{
	"ready-reviewer": DefaultReviewerTools,
}

// markEmbedded records the embedded fallback as the source for fields no tier
// supplied but that the *Binding resolvers fill from the binary's defaults, so
// a display surface attributes them honestly rather than showing them unsourced.
// It reports only — the resolvers stay the single owner of the actual defaulting.
func markEmbedded(section string, b AgentBinding, src map[string]string) {
	if strings.TrimSpace(b.Command) == "" {
		src["command"] = SourceEmbedded
	}
	// Only [reviewer] gets an embedded tools grant (DefaultReviewerTools) from the
	// *Binding resolvers, and ResolveAgentsBaseline adds the seats in
	// embeddedSeatTools; other named bindings resolve a command but keep an empty
	// grant.
	if section == "reviewer" && strings.TrimSpace(b.Tools) == "" {
		src["tools"] = SourceEmbedded
	}
}

func mergeStringMap(base, top map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(top))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range top {
		out[k] = v
	}
	return out
}

func mergeAnyMap(base, top map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(top))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range top {
		out[k] = v
	}
	return out
}
