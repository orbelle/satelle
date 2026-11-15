package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type Settings struct {
	Workflows workflow.Rules  `toml:"workflows"`
	Gate      GateSettings    `toml:"gate"`
	Hook      HookSettings    `toml:"hook"`
	Rules     RuleSettings    `toml:"rules"`
	Items     ItemSettings    `toml:"items"`
	Service   ServiceSettings `toml:"service"`
	Sync      SyncSettings    `toml:"sync"`
	Ledger    LedgerSettings  `toml:"ledger"`
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

type RuleSettings struct {
	ReviewerDiffersFromPerformer bool `toml:"reviewer_differs_from_performer"`
}

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
	Root      string
	Settings  Settings
	Workflows map[string]*workflow.Workflow
	Skills    map[string]workflow.Skill
	Agents    agent.Catalog
	Queues    map[string]*workflow.Queue
	Doctor    DoctorSettings
	Volatile  substrate.VolatileRules
}

type DoctorSettings struct {
	Checks  []DoctorCheck `toml:"check"`
	Open    []DoctorOpen  `toml:"items"`
	Resolve DoctorResolve `toml:"resolve"`
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
	SettingsFile = "satelle.toml"
	DoctorFile   = "doctor.toml"
	VolatileFile = "volatile.toml"
	AgentsFile   = "agents.toml"
	SessionFile  = "session.md"
	WorkflowsDir = "workflows"
	SkillsDir    = "skills"
	QueuesDir    = "queues"
	RemotesDir   = "remotes"
	RemoteFile   = "remote.toml"
)

func LoadSettings(root string) (Settings, error) {
	var st Settings
	err := workflow.DecodeStrict(filepath.Join(root, SettingsFile), &st)
	return st, err
}

func LoadConfig(root string) (*Config, error) {
	c := &Config{Root: root, Workflows: map[string]*workflow.Workflow{}, Skills: map[string]workflow.Skill{}, Agents: agent.Catalog{},
		Queues: map[string]*workflow.Queue{}}
	var errs []error
	if err := legacyProblem(root); err != nil {
		errs = append(errs, err)
	}
	st, err := LoadSettings(root)
	c.Settings = st
	if err != nil {
		errs = append(errs, err)
	}
	if p := filepath.Join(root, DoctorFile); exists(p) {
		if err := workflow.DecodeStrict(p, &c.Doctor); err != nil {
			errs = append(errs, err)
		}
	}
	if p := filepath.Join(root, VolatileFile); exists(p) {
		if err := workflow.DecodeStrict(p, &c.Volatile); err != nil {
			errs = append(errs, err)
		}
	}
	if p := filepath.Join(root, AgentsFile); exists(p) {
		cat, err := agent.LoadCatalog(p)
		if err != nil {
			errs = append(errs, err)
		} else {
			c.Agents = cat
		}
	}
	if wfs, err := workflow.Load(filepath.Join(root, WorkflowsDir)); err != nil {
		errs = append(errs, err)
		if wfs != nil {
			c.Workflows = wfs
		}
	} else {
		c.Workflows = wfs
	}
	if sk, err := workflow.LoadSkills(filepath.Join(root, SkillsDir)); err != nil {
		errs = append(errs, err)
		if sk != nil {
			c.Skills = sk
		}
	} else {
		c.Skills = sk
	}
	if qs, err := workflow.LoadQueues(filepath.Join(root, QueuesDir)); err != nil {
		errs = append(errs, err)
		if qs != nil {
			c.Queues = qs
		}
	} else {
		c.Queues = qs
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
	errs = append(errs, c.Settings.Workflows.Validate(func(n string) bool { _, ok := c.Workflows[n]; return ok })...)
	errs = append(errs, c.Agents.Validate()...)
	skill := func(n string) (workflow.Skill, bool) { s, ok := c.Skills[n]; return s, ok }
	for _, name := range sortedKeys(c.Workflows) {
		w := c.Workflows[name]
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
	}
	for _, n := range sortedKeys(c.Skills) {
		errs = append(errs, c.Skills[n].Problems()...)
	}
	errs = append(errs, c.queueProblems()...)
	errs = append(errs, c.doctorProblems()...)
	for _, err := range c.Volatile.Problems() {
		errs = append(errs, fmt.Errorf("%s: %w", VolatileFile, err))
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
			errs = append(errs, fmt.Errorf("%s: check %d names skill %q, which has no %s/%s.md", DoctorFile, i+1, ch.Skill, SkillsDir, ch.Skill))
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
	return errs
}

func (c *Config) queueProblems() []error {
	var errs []error
	wf := func(n string) bool { _, ok := c.Workflows[n]; return ok }
	bound := map[string]string{}
	for _, name := range sortedKeys(c.Queues) {
		q := c.Queues[name]
		errs = append(errs, q.Validate(wf)...)
		id := q.ChainID(c.Settings.Prefix(item.QueueKind))
		if other, dup := bound[id]; dup {
			errs = append(errs, fmt.Errorf("%s: binds queue %s, which %s also binds", q.File, id, c.Queues[other].File))
			continue
		}
		bound[id] = name
	}
	return errs
}

func (c *Config) bindingProblems(w *workflow.Workflow) []error {
	var errs []error
	for _, gname := range sortedKeys(w.Gates) {
		for _, j := range w.Gates[gname].Judges() {
			if b, ok := c.Agents[j]; ok {
				if err := agent.CheckJudge(b); err != nil {
					errs = append(errs, fmt.Errorf("workflow %s: gate %q: %w", w.Name, gname, err))
				}
			}
		}
		g := w.Gates[gname]
		if sk, ok := c.Skills[g.Skill]; ok && len(g.Judges()) > 0 && !sk.IsCheck() {
			if key, legacy := gate.LegacyAnswerKey(sk.Body); legacy {
				errs = append(errs, fmt.Errorf("workflow %s: gate %q: skill %q answers with %q; a judge answers {\"verdict\":\"accept\"|\"reject\",\"notes\":\"...\"}, and an answer without verdict is a reject",
					w.Name, gname, g.Skill, key))
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
	if !c.Settings.Rules.ReviewerDiffersFromPerformer {
		return errs
	}
	for i, t := range w.Transitions {
		for _, from := range expandFrom(w, t.From) {
			p := w.States[from].Agent()
			if p == nil {
				continue
			}
			model := c.Agents[p.Agent].Model
			for _, g := range t.Groups {
				for _, gname := range g.Gates {
					for _, j := range w.Gates[gname].Judges() {
						if model != "" && c.Agents[j].Model == model {
							errs = append(errs, fmt.Errorf("workflow %s: transition %d: gate %q judge %q runs model %q, the same as the performer of %q",
								w.Name, i+1, gname, j, model, from))
						}
					}
				}
			}
		}
	}
	return errs
}

func expandFrom(w *workflow.Workflow, from []string) []string {
	var out []string
	for _, f := range from {
		if f != workflow.AnyState {
			out = append(out, f)
			continue
		}
		for _, n := range sortedKeys(w.States) {
			if st := w.States[n]; !st.Terminal && !st.Park {
				out = append(out, n)
			}
		}
	}
	return out
}

func Validate(root string) []error {
	c, err := LoadConfig(root)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	return append(errs, c.Problems()...)
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
		if s, ok := c.Skills[g.Skill]; ok && !s.IsCheck() {
			bad("placement %q: skill %q is not a check; only a check gate runs on a remote", p, g.Skill)
		}
		file := filepath.Join(c.Root, RemotesDir, id, RemoteFile)
		b, err := os.ReadFile(file)
		if err != nil {
			bad("is placed on remote %s, which has no %s: %v", id, filepath.Join(filepath.Base(c.Root), RemotesDir, id, RemoteFile), err)
			continue
		}
		if _, err := remotedef.Parse(b); err != nil {
			bad("is placed on remote %s: %v", id, err)
		}
	}
	return errs
}
