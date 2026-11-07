package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// Settings is satelle.toml.
type Settings struct {
	Workflows workflow.Rules  `toml:"workflows"`
	Gate      GateSettings    `toml:"gate"`
	Hook      HookSettings    `toml:"hook"`
	Rules     RuleSettings    `toml:"rules"`
	Items     ItemSettings    `toml:"items"`
	Service   ServiceSettings `toml:"service"`
	Sync      SyncSettings    `toml:"sync"`
}

// GateSettings is the [gate] section, read by the edit gate.
type GateSettings struct {
	EditExemptPaths []string `toml:"edit_exempt_paths"`
	LockedPaths     []string `toml:"locked_paths"`
}

// HookSettings is the [hook] section: files the context hook prints.
type HookSettings struct {
	Inject []string `toml:"inject"`
}

// RuleSettings is the [rules] section.
type RuleSettings struct {
	ReviewerDiffersFromPerformer bool `toml:"reviewer_differs_from_performer"`
}

// ItemSettings is the [items] section: id prefix per kind.
type ItemSettings struct {
	Prefix map[string]string `toml:"prefix"`
}

// ServiceSettings is the [service] section (satelled).
type ServiceSettings struct {
	Port int `toml:"port"`
}

// SyncSettings is the [sync] section.
type SyncSettings struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
}

// Config is everything a repo configures under its .satelle/ directory.
type Config struct {
	Root      string
	Settings  Settings
	Workflows map[string]*workflow.Workflow
	Skills    map[string]workflow.Skill
	Agents    agent.Catalog
	Queues    map[string]*workflow.Queue // by name
}

// Config file and directory names under the root.
const (
	SettingsFile = "satelle.toml"
	AgentsFile   = "agents.toml"
	WorkflowsDir = "workflows"
	SkillsDir    = "skills"
	QueuesDir    = "queues"
)

// LoadConfig reads root's configuration. It returns what it could load and
// every load error; Problems adds the validation errors.
func LoadConfig(root string) (*Config, error) {
	c := &Config{Root: root, Workflows: map[string]*workflow.Workflow{}, Skills: map[string]workflow.Skill{}, Agents: agent.Catalog{},
		Queues: map[string]*workflow.Queue{}}
	var errs []error
	if err := legacyProblem(root); err != nil {
		errs = append(errs, err)
	}
	if err := workflow.DecodeStrict(filepath.Join(root, SettingsFile), &c.Settings); err != nil {
		errs = append(errs, err)
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

// Prefix is the id prefix of kind: [items] prefix, else the kind's first
// three letters.
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

// QueueIndex binds each queue file to the ledger chain it defines: chain id
// to definition. It is the one owner of that binding.
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

// Problems returns every validation problem of a loaded configuration.
func (c *Config) Problems() []error {
	var errs []error
	errs = append(errs, c.Settings.Workflows.Validate(func(n string) bool { _, ok := c.Workflows[n]; return ok })...)
	errs = append(errs, c.Agents.Validate()...)
	skill := func(n string) (workflow.Skill, bool) { s, ok := c.Skills[n]; return s, ok }
	for _, name := range sortedKeys(c.Workflows) {
		w := c.Workflows[name]
		errs = append(errs, w.Validate(skill, c.Agents.Has)...)
		errs = append(errs, c.bindingProblems(w)...)
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
	errs = append(errs, c.queueProblems()...)
	return errs
}

// queueProblems checks each queue file, and that no two bind the same chain.
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

// bindingProblems checks what the catalog alone cannot: judges may judge,
// dispatched performers can carry the performer denies, and (when the rule is
// on) no judge runs its performer's model.
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

// Validate loads and validates root's configuration, returning every problem.
func Validate(root string) []error {
	c, err := LoadConfig(root)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	return append(errs, c.Problems()...)
}
