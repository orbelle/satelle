package kernel

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type Settings struct {
	Workflows workflow.Rules   `toml:"workflows"`
	Gate      GateSettings     `toml:"gate"`
	Hook      HookSettings     `toml:"hook"`
	Items     ItemSettings     `toml:"items"`
	Service   ServiceSettings  `toml:"service"`
	Sync      SyncSettings     `toml:"sync"`
	Ledger    LedgerSettings   `toml:"ledger"`
	Review    workflow.Review  `toml:"review"`
	Models    ModelSettings    `toml:"models"`
	Dispatch  DispatchSettings `toml:"dispatch"`
}

type DispatchSettings struct {
	TargetEnv []string `toml:"target_env"`
}

func (d DispatchSettings) problems() []error {
	var errs []error
	for _, n := range d.TargetEnv {
		if err := config.TargetEnvProblem(n); err != nil {
			errs = append(errs, fmt.Errorf("%s [dispatch] target_env: %w", config.ConfigName, err))
		}
	}
	return errs
}

type ModelSettings struct {
	Aliases map[string]string `toml:"aliases"`
}

type GateSettings struct {
	EditExemptPaths []string `toml:"edit_exempt_paths"`
	LockedPaths     []string `toml:"locked_paths"`
}

type HookSettings struct {
	Inject  []string `toml:"inject"`
	Refresh *bool    `toml:"refresh"`
}

func (h HookSettings) Refreshes() bool { return h.Refresh == nil || *h.Refresh }

type ItemSettings struct {
	Prefix map[string]string `toml:"prefix"`
}

type ServiceSettings struct {
	Port int `toml:"port"`
}

type SyncSettings struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
}

type LedgerSettings struct {
	Store ledgerstore.Spec `toml:"store"`
}

type Config struct {
	Root         string
	Settings     Settings
	Workflows    map[string]*workflow.Workflow
	Skills       map[string]workflow.Skill
	Packs        map[string]workflow.Pack
	Agents       agent.Catalog
	Overrides    []agent.Override
	Queues       map[string]*workflow.Queue
	Doctor       DoctorSettings
	Volatile     substrate.VolatileRules
	UI           UISettings
	Init         InitSettings
	Harness      map[string]HarnessSettings
	Repos        reporeg.Registry
	Parts        map[string]*Part
	PartWarnings []PartWarning
	UnknownKeys  []workflow.UnknownKey
}

type HarnessSettings struct {
	ContextLimitBytes int `toml:"context_limit_bytes"`
}

type UISettings struct {
	TagColours map[string]string `toml:"tag_colours"`
}

type DoctorSettings struct {
	Checks  []DoctorCheck `toml:"check"`
	Open    []DoctorOpen  `toml:"items"`
	Resolve DoctorResolve `toml:"resolve"`
	Repos   DoctorRepos   `toml:"repos"`
}

type DoctorRepos struct {
	MaxCount *int `toml:"max_count"`
}

type DoctorResolve struct {
	Kind     string `toml:"kind"`
	Tag      string `toml:"tag"`
	Category string `toml:"category"`
}

func (r DoctorResolve) Selection() item.Selection {
	return item.Selection{Kind: r.Kind, Tag: r.Tag, Category: r.Category}
}

func (r DoctorResolve) Unset() []string {
	var out []string
	if r.Kind == "" {
		out = append(out, "kind")
	}
	if r.Tag == "" {
		out = append(out, "tag")
	}
	return out
}

type DoctorCheck struct {
	Skill string `toml:"skill"`
	Fix   string `toml:"fix"`
}

type DoctorOpen struct {
	Name     string `toml:"name"`
	Kind     string `toml:"kind"`
	Status   string `toml:"status"`
	Tag      string `toml:"tag"`
	Category string `toml:"category"`
	Fix      string `toml:"fix"`
}

func (o DoctorOpen) Selection() item.Selection {
	return item.Selection{Kind: o.Kind, Status: o.Status, Tag: o.Tag, Category: o.Category}
}

const (
	SettingsFile    = "satelle.toml"
	PacksFile       = "packs.toml"
	PackMergeDir    = "pack-merge"
	DoctorFile      = "doctor.toml"
	VolatileFile    = "volatile.toml"
	UIFile          = "ui.toml"
	HarnessFile     = "harness.toml"
	AgentsFile      = "agents.toml"
	AgentsLocalFile = "agents.local.toml"
	ReposFile       = reporeg.TrackedFile
	ReposLocalFile  = reporeg.LocalFile
	SessionFile     = "session.md"
	AutoModeFile    = "auto-mode.md"
	WorkflowsDir    = "workflows"
	PartsDir        = "repos"
	SkillsDir       = "skills"
	QueuesDir       = "queues"
	RemotesDir      = "remotes"
	RemoteFile      = "remote.toml"
)

func LoadAgents(root string) (agent.Catalog, []agent.Override, error) {
	cat := agent.Catalog{}
	if p := filepath.Join(root, AgentsFile); exists(p) {
		c, err := agent.LoadCatalog(p)
		if err != nil {
			return cat, nil, err
		}
		cat = c
	}
	p := filepath.Join(root, AgentsLocalFile)
	if !exists(p) {
		return cat, nil, nil
	}
	merged, ovs, err := cat.Override(p)
	if err != nil {
		var errs []error
		for _, e := range joinedErrors(err) {
			errs = append(errs, fmt.Errorf("%s: %w", AgentsLocalFile, e))
		}
		return cat, nil, errors.Join(errs...)
	}
	return merged, ovs, nil
}

func (c *Config) override(name string) (agent.Override, bool) {
	for _, o := range c.Overrides {
		if o.Name == name {
			return o, true
		}
	}
	return agent.Override{}, false
}

func (c *Config) agentProblems() []error {
	var errs []error
	for _, name := range c.Agents.Names() {
		o, overridden := c.override(name)
		for _, err := range append(agent.Problems(c.Agents[name]), c.Agents.FallbackProblems(name)...) {
			if overridden {
				errs = append(errs, fmt.Errorf("%s: agents.%s: %w (overriding %s)", AgentsLocalFile, name, err, strings.Join(o.Fields, ", ")))
			} else {
				errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
			}
		}
	}
	return errs
}

var (
	lenientSettingsTables = []string{"hook", "items", "service", "sync", "ledger"}
)

func isRetiredRulesKey(u workflow.UnknownKey) bool {
	return u.Table == "rules" || strings.HasPrefix(u.Table, "rules.") || strings.HasPrefix(u.Table, "rules[") || (u.Table == "" && u.Key == "rules")
}

func isLenientSettingsKey(u workflow.UnknownKey) bool {
	for _, t := range lenientSettingsTables {
		if u.Table == t || strings.HasPrefix(u.Table, t+".") {
			return true
		}
	}
	return false
}

func strictKeyName(u workflow.UnknownKey) string {
	if u.Table == "" {
		return u.Key
	}
	return u.Table + "." + u.Key
}

func loadSettings(root string) (Settings, []workflow.UnknownKey, error) {
	var st Settings
	path := filepath.Join(root, SettingsFile)
	unknown, err := workflow.Decode(path, &st)
	if err != nil {
		if retired := retiredRules(path); retired != nil {
			return st, nil, retired
		}
		return st, nil, err
	}
	var lenient []workflow.UnknownKey
	var strictKeys []string
	hasRules := false
	for _, u := range unknown {
		switch {
		case isRetiredRulesKey(u):
			hasRules = true
		case isLenientSettingsKey(u):
			lenient = append(lenient, u)
		default:
			strictKeys = append(strictKeys, strictKeyName(u))
		}
	}
	var errs []error
	if hasRules {
		if retired := retiredRules(path); retired != nil {
			errs = append(errs, retired)
		}
	}
	if len(strictKeys) > 0 {
		errs = append(errs, fmt.Errorf("%s: unknown key %s", path, strings.Join(strictKeys, ", ")))
	}
	for _, from := range slices.Sorted(maps.Keys(st.Models.Aliases)) {
		for _, s := range []string{from, st.Models.Aliases[from]} {
			if modelSlug(s) == "" {
				errs = append(errs, fmt.Errorf("satelle.toml: [models] aliases: %q normalises to an empty identifier", s))
			}
		}
	}
	errs = append(errs, st.Dispatch.problems()...)
	if len(errs) > 0 {
		return st, nil, errors.Join(errs...)
	}
	return st, lenient, nil
}

func LoadSettings(root string) (Settings, error) {
	st, _, err := loadSettings(root)
	return st, err
}

func normalizeUnknownKeys(root string, keys []workflow.UnknownKey) []workflow.UnknownKey {
	if len(keys) == 0 {
		return nil
	}
	clean := filepath.Clean(root)
	base := filepath.Base(clean)
	out := make([]workflow.UnknownKey, len(keys))
	for i, u := range keys {
		if rel, err := filepath.Rel(clean, u.File); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			u.File = filepath.ToSlash(filepath.Join(base, rel))
		}
		out[i] = u
	}
	slices.SortFunc(out, func(a, b workflow.UnknownKey) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Table, b.Table),
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.Value, b.Value),
		)
	})
	return slices.Compact(out)
}

func LoadConfig(root string) (*Config, error) {
	return loadConfig(root, filepath.Join(root, SkillsDir))
}

func loadConfig(root, skillsDir string) (*Config, error) {
	c := &Config{Root: root, Workflows: map[string]*workflow.Workflow{}, Skills: map[string]workflow.Skill{}, Packs: map[string]workflow.Pack{}, Agents: agent.Catalog{},
		Queues: map[string]*workflow.Queue{}}
	var errs []error
	var unknown []workflow.UnknownKey
	if err := legacyProblem(root); err != nil {
		errs = append(errs, err)
	}
	st, un, err := loadSettings(root)
	c.Settings = st
	unknown = append(unknown, un...)
	if err != nil {
		errs = append(errs, err)
	}
	if p := filepath.Join(root, DoctorFile); exists(p) {
		un, err := workflow.Decode(p, &c.Doctor)
		unknown = append(unknown, un...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if s, un, err := loadInit(root); err != nil {
		c.Init = s
		unknown = append(unknown, un...)
		errs = append(errs, err)
	} else {
		c.Init = s
		unknown = append(unknown, un...)
	}
	if p := filepath.Join(root, VolatileFile); exists(p) {
		un, err := workflow.Decode(p, &c.Volatile)
		unknown = append(unknown, un...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if p := filepath.Join(root, UIFile); exists(p) {
		un, err := workflow.Decode(p, &c.UI)
		unknown = append(unknown, un...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if p := filepath.Join(root, HarnessFile); exists(p) {
		un, err := workflow.Decode(p, &c.Harness)
		unknown = append(unknown, un...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if cat, ovs, err := LoadAgents(root); err != nil {
		errs = append(errs, err)
	} else {
		c.Agents, c.Overrides = cat, ovs
	}
	reg, err := reporeg.Load(root)
	c.Repos = reg
	if err != nil {
		errs = append(errs, err)
	}
	if wfs, err := workflow.Load(filepath.Join(root, WorkflowsDir)); err != nil {
		errs = append(errs, err)
		if wfs != nil {
			c.Workflows = wfs
		}
	} else {
		c.Workflows = wfs
	}
	if sk, err := workflow.LoadSkills(skillsDir); err != nil {
		errs = append(errs, err)
		if sk != nil {
			c.Skills = sk
		}
	} else {
		c.Skills = sk
	}
	if pks, un, err := workflow.LoadPacks(skillsDir); err != nil {
		errs = append(errs, err)
		if pks != nil {
			c.Packs = pks
		}
		unknown = append(unknown, un...)
	} else {
		c.Packs = pks
		unknown = append(unknown, un...)
	}
	c.UnknownKeys = normalizeUnknownKeys(root, unknown)
	if qs, err := workflow.LoadQueues(filepath.Join(root, QueuesDir)); err != nil {
		errs = append(errs, err)
		if qs != nil {
			c.Queues = qs
		}
	} else {
		c.Queues = qs
	}
	if err := c.loadParts(); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func (s Settings) Prefix(kind string) string {
	if p := s.Items.Prefix[kind]; p != "" {
		return p
	}
	r := []rune(strings.ToLower(kind))
	if len(r) > 3 {
		r = r[:3]
	}
	return string(r)
}

func QueueIndex(s Settings, queues map[string]*workflow.Queue) map[string]*workflow.Queue {
	out := map[string]*workflow.Queue{}
	for _, name := range sortedKeys(queues) {
		q := queues[name]
		id := q.ChainID(s.Prefix(item.QueueKind))
		if _, dup := out[id]; !dup {
			out[id] = q
		}
	}
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func (c *Config) Problems() []error {
	var errs []error
	errs = append(errs, c.Settings.Workflows.Validate(func(n string) bool { w, ok := c.Workflows[n]; return ok && w.Part == "" })...)
	errs = append(errs, c.Settings.Review.Validate(c.reviewGate, c.Agents.Has)...)
	errs = append(errs, c.agentProblems()...)
	for _, name := range sortedKeys(c.Workflows) {
		errs = append(errs, c.workflowProblems(c.Workflows[name])...)
	}
	for _, n := range sortedKeys(c.Skills) {
		errs = append(errs, c.Skills[n].Problems()...)
	}
	errs = append(errs, c.packProblems()...)
	errs = append(errs, c.queueProblems()...)
	errs = append(errs, c.doctorProblems()...)
	for _, err := range c.Volatile.Problems() {
		errs = append(errs, fmt.Errorf("%s: %w", VolatileFile, err))
	}
	shipped, _ := ShippedFiles()
	errs = append(errs, c.Init.problems(shipped)...)
	errs = append(errs, c.harnessProblems()...)
	errs = append(errs, c.Repos.Problems()...)
	return append(errs, c.UI.problems()...)
}

func (c *Config) workflowProblems(w *workflow.Workflow) []error {
	var errs []error
	skill := func(n string) (workflow.Skill, bool) { return c.skillOf(w.Part, n) }
	errs = append(errs, w.Validate(skill, c.Agents.Has)...)
	errs = append(errs, c.bindingProblems(w)...)
	errs = append(errs, c.gatePlacementProblems(w)...)
	for _, sname := range sortedKeys(w.States) {
		if typ, ok := w.States[sname].Operated(); ok && !agent.ValidType(typ) {
			errs = append(errs, fmt.Errorf("workflow %s: state %q operator type %q is not a valid attachment type (letters, digits, '.', '_', '-')",
				w.Name, sname, typ))
		}
		for _, q := range w.States[sname].Enqueue {
			if _, ok := c.Queues[q]; !ok {
				errs = append(errs, fmt.Errorf("workflow %s: state %q enqueues into %q, which has no %s/%s/%s.toml",
					w.Name, sname, q, filepath.Base(c.Root), QueuesDir, q))
			}
		}
	}
	if w.Part != "" {
		for i, e := range errs {
			errs[i] = partSafe(e)
		}
	}
	return errs
}

func (c *Config) harnessProblems() []error {
	var errs []error
	for _, name := range sortedKeys(c.Harness) {
		if !slices.Contains(hook.Harnesses, name) {
			errs = append(errs, fmt.Errorf("%s: [%s] is not a harness (%s)", HarnessFile, name, strings.Join(hook.Harnesses, ", ")))
			continue
		}
		if n := c.Harness[name].ContextLimitBytes; n <= 0 {
			errs = append(errs, fmt.Errorf("%s: [%s] context_limit_bytes = %d is not a positive byte count; a harness with no limit has no table", HarnessFile, name, n))
		}
	}
	return errs
}

func (u UISettings) problems() []error {
	var errs []error
	for _, tag := range sortedKeys(u.TagColours) {
		colour := u.TagColours[tag]
		if strings.TrimSpace(tag) == "" {
			errs = append(errs, fmt.Errorf("%s: [tag_colours] has an entry with an empty tag", UIFile))
			continue
		}
		if !slices.Contains(item.ChipColours, colour) {
			errs = append(errs, fmt.Errorf("%s: [tag_colours] %q = %q is not a palette colour (%s)", UIFile, tag, colour, strings.Join(item.ChipColours, ", ")))
		}
	}
	return errs
}

func (c *Config) VolatileFindings() ([]substrate.Finding, error) {
	return substrate.Volatile(c.Root, c.Volatile)
}

func (c *Config) doctorProblems() []error {
	var errs []error
	for i, ch := range c.Doctor.Checks {
		sk, ok := c.Skills[ch.Skill]
		switch {
		case ch.Skill == "":
			errs = append(errs, fmt.Errorf("%s: check %d names no skill", DoctorFile, i+1))
		case !ok:
			errs = append(errs, fmt.Errorf("%s: check %d names skill %q, which has no %s/%s.md or %s/%s/%s", DoctorFile, i+1, ch.Skill, SkillsDir, ch.Skill, SkillsDir, ch.Skill, workflow.SkillFile))
		case !sk.IsCheck():
			errs = append(errs, fmt.Errorf("%s: check %d names skill %q, which is not a check (type: check)", DoctorFile, i+1, ch.Skill))
		}
		if strings.TrimSpace(ch.Fix) == "" {
			errs = append(errs, fmt.Errorf("%s: check %d (%s) has no fix", DoctorFile, i+1, ch.Skill))
		}
	}
	seen := map[string]bool{}
	for i, o := range c.Doctor.Open {
		switch {
		case o.Name == "":
			errs = append(errs, fmt.Errorf("%s: items %d has no name", DoctorFile, i+1))
		case seen[o.Name]:
			errs = append(errs, fmt.Errorf("%s: items %q is named twice", DoctorFile, o.Name))
		}
		seen[o.Name] = true
		if o.Kind == "" && o.Tag == "" && o.Category == "" {
			errs = append(errs, fmt.Errorf("%s: items %q selects nothing: give at least one of kind, tag or category", DoctorFile, o.Name))
		}
		if strings.TrimSpace(o.Fix) == "" {
			errs = append(errs, fmt.Errorf("%s: items %q has no fix", DoctorFile, o.Name))
		}
	}
	if m := c.Doctor.Repos.MaxCount; m != nil && *m <= 0 {
		errs = append(errs, fmt.Errorf("%s: [repos] max_count must be a positive integer", DoctorFile))
	}
	return errs
}

func (c *Config) packProblems() []error {
	var errs []error
	skill := func(n string) (workflow.Skill, bool) { s, ok := c.Skills[n]; return s, ok }
	seenName := map[string]workflow.Pack{}
	for _, entry := range sortedKeys(c.Packs) {
		pk := c.Packs[entry]
		errs = append(errs, pk.Validate(skill)...)
		name := strings.TrimSpace(pk.Name)
		if name == "" {
			continue
		}
		if other, dup := seenName[name]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate pack name %q (also defined in %s)", pk.File, name, other.File))
			continue
		}
		seenName[name] = pk
	}
	return errs
}

func (c *Config) queueProblems() []error {
	var errs []error
	wf := func(n string) bool { w, ok := c.Workflows[n]; return ok && w.Part == "" }
	bound := map[string]string{}
	for _, name := range sortedKeys(c.Queues) {
		q := c.Queues[name]
		errs = append(errs, q.Validate(wf, c.Repos.Targets())...)
		id := q.ChainID(c.Settings.Prefix(item.QueueKind))
		if other, dup := bound[id]; dup {
			errs = append(errs, fmt.Errorf("%s: binds queue %s, which %s also binds", q.File, id, c.Queues[other].File))
			continue
		}
		bound[id] = name
	}
	return errs
}

func (c *Config) judges(gname string, g workflow.Gate) []string {
	out := slices.Clone(g.Judges())
	for _, a := range c.Settings.Review.Agents(gname) {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

type AgentUse struct {
	Workflow string
	State    string
	Gate     string
	Role     string
	Of       string
}

func (u AgentUse) String() string {
	switch u.Role {
	case "performer":
		return fmt.Sprintf("workflow %s state %s (performer)", u.Workflow, u.State)
	case "fallback":
		return fmt.Sprintf("workflow %s gate %s (fallback of %s)", u.Workflow, u.Gate, u.Of)
	}
	return fmt.Sprintf("workflow %s gate %s (%s)", u.Workflow, u.Gate, u.Role)
}

func (c *Config) AgentUses() map[string][]AgentUse {
	out := map[string][]AgentUse{}
	for _, wname := range sortedKeys(c.Workflows) {
		w := c.Workflows[wname]
		for _, sname := range sortedKeys(w.States) {
			if p := w.States[sname].Agent(); p != nil && p.Agent != "" {
				out[p.Agent] = append(out[p.Agent], AgentUse{Workflow: wname, State: sname, Role: "performer"})
			}
		}
		for _, gname := range sortedKeys(w.Gates) {
			for _, j := range c.judges(gname, w.Gates[gname]) {
				out[j] = append(out[j], AgentUse{Workflow: wname, Gate: gname, Role: "judge"})
				if f := c.Agents[j].Fallback; f != "" {
					if _, ok := c.Agents[f]; ok {
						out[f] = append(out[f], AgentUse{Workflow: wname, Gate: gname, Role: "fallback", Of: j})
					}
				}
			}
		}
	}
	return out
}

func (c *Config) judgeSets(gname string, g workflow.Gate) [][]string {
	sets := [][]string{g.Judges()}
	panels := c.Settings.Review.Panels[gname]
	for _, tier := range sortedKeys(panels) {
		sets = append(sets, panels[tier])
	}
	return sets
}

func (c *Config) fallbackProblems(w *workflow.Workflow, gname string) []error {
	var errs []error
	g := w.Gates[gname]
	for _, j := range c.judges(gname, g) {
		f := c.Agents[j].Fallback
		fb, ok := c.Agents[f]
		if f == "" || !ok {
			continue
		}
		if err := agent.CheckJudge(fb); err != nil {
			errs = append(errs, fmt.Errorf("workflow %s: gate %q: judge %q fallback: %w", w.Name, gname, j, err))
		}
	}
	for _, set := range c.judgeSets(gname, g) {
		named := map[string]string{}
		for _, j := range set {
			f := c.Agents[j].Fallback
			if f == "" {
				continue
			}
			if slices.Contains(set, f) {
				errs = append(errs, fmt.Errorf("workflow %s: gate %q: judge %q fallback %q is also a judge of this gate", w.Name, gname, j, f))
			}
			if first, dup := named[f]; dup && first != j {
				errs = append(errs, fmt.Errorf("workflow %s: gate %q: judges %q and %q name the same fallback %q, so one binding could answer for both", w.Name, gname, first, j, f))
			} else if !dup {
				named[f] = j
			}
		}
	}
	return errs
}

func (c *Config) reviewGate(name string) (found, check bool) {
	check = true
	for _, wname := range sortedKeys(c.Workflows) {
		if c.Workflows[wname].Part != "" {
			continue
		}
		g, ok := c.Workflows[wname].Gates[name]
		if !ok {
			continue
		}
		found = true
		if sk, ok := c.skillOf(c.Workflows[wname].Part, g.Skill); !ok || !sk.IsCheck() {
			check = false
		}
	}
	return found, found && check
}

func (c *Config) bindingProblems(w *workflow.Workflow) []error {
	var errs []error
	for _, gname := range sortedKeys(w.Gates) {
		for _, j := range c.judges(gname, w.Gates[gname]) {
			if b, ok := c.Agents[j]; ok {
				if err := agent.CheckJudge(b); err != nil {
					errs = append(errs, fmt.Errorf("workflow %s: gate %q: %w", w.Name, gname, err))
				}
			}
		}
		errs = append(errs, c.fallbackProblems(w, gname)...)
		g := w.Gates[gname]
		if sk, ok := c.skillOf(w.Part, g.Skill); ok {
			if err := legacyAnswerProblem(w.Name, gname, g, sk); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, sname := range sortedKeys(w.States) {
		p := w.States[sname].Agent()
		if p == nil {
			continue
		}
		b, ok := c.Agents[p.Agent]
		if !ok || agent.IsSession(b) {
			continue
		}
		if a, err := agent.AdapterFor(b); err == nil {
			if _, err := a.Argv(agent.Request{Binding: b, Deny: agent.PerformerDenies}); err != nil {
				errs = append(errs, fmt.Errorf("workflow %s: state %q performer %q: %w", w.Name, sname, p.Agent, err))
			}
		}
	}
	return errs
}

func Validate(root string) []error {
	return validate(LoadConfig(root))
}

func ValidateWithSkills(root, skillsDir string) []error {
	return validate(loadConfig(root, skillsDir))
}

func validate(c *Config, err error) []error {
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	return append(errs, c.Problems()...)
}

func RemoteFiles(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, RemotesDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := path.Join(RemotesDir, e.Name(), RemoteFile)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func RemoteDefinitionProblems(root string) []error {
	var errs []error
	for _, rel := range RemoteFiles(root) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		for _, f := range remotedef.Errors(remotedef.Validate(b)) {
			errs = append(errs, fmt.Errorf("%s: %s: %s", rel, f.Key, f.Message))
		}
	}
	return errs
}

func (c *Config) gatePlacementProblems(w *workflow.Workflow) []error {
	var errs []error
	for _, gname := range sortedKeys(w.Gates) {
		g := w.Gates[gname]
		p := g.Placement
		if p == "" || p == agent.PlaceLocal {
			continue
		}
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("workflow %s: gate %q: "+format, append([]any{w.Name, gname}, args...)...))
		}
		id, ok := strings.CutPrefix(p, agent.PlaceRemote)
		if !ok || id == "" {
			bad("placement %q is not %s or %s<id>", p, agent.PlaceLocal, agent.PlaceRemote)
			continue
		}
		if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
			bad("placement %q: %q is not a remote id", p, id)
			continue
		}
		if s, ok := c.skillOf(w.Part, g.Skill); ok && !s.IsCheck() {
			bad("placement %q: skill %q is not a check; only a check gate runs on a remote", p, g.Skill)
		}
		file := filepath.Join(c.Root, RemotesDir, id, RemoteFile)
		b, err := os.ReadFile(file)
		if err != nil {
			bad("is placed on remote %s, which has no %s: %v", id, filepath.ToSlash(filepath.Join(filepath.Base(c.Root), RemotesDir, id, RemoteFile)), err)
			continue
		}
		if _, err := remotedef.Parse(b); err != nil {
			bad("is placed on remote %s: %v", id, err)
		}
	}
	return errs
}
