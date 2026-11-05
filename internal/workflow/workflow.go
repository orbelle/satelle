// Package workflow reads the workflows a repo configures: states, gates and
// explicit transitions, parsed strictly and validated. No state, gate or skill
// name is known here; every name comes from configuration.
package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Mechanism tokens of the schema.
const (
	AnyState    = "*"      // in a transition's from: any non-terminal, non-park state
	Prior       = "@prior" // as a transition's to: back to the state a resuming park was entered from
	Sequential  = "sequential"
	Parallel    = "parallel"
	CombineAll  = "all"
	CombineAny  = "any"
	CombineMost = "majority"
)

// Workflow is one workflows/<name>.toml.
type Workflow struct {
	Name        string           `toml:"name"`
	States      map[string]State `toml:"states"`
	Gates       map[string]Gate  `toml:"gates"`
	Transitions []Transition     `toml:"transition"`
	File        string           `toml:"-"`
}

// State is one state of a workflow.
type State struct {
	Start     bool       `toml:"start"`
	Terminal  bool       `toml:"terminal"`
	Park      bool       `toml:"park"`
	Resumes   bool       `toml:"resumes"`
	Performer *Performer `toml:"performer"`
}

// Performer is the binding that does a state's work, and the skill it follows.
type Performer struct {
	Agent string `toml:"agent"`
	Skill string `toml:"skill"`
}

// Gate is a named judgement a transition can carry.
type Gate struct {
	Skill   string   `toml:"skill"`
	Agent   string   `toml:"agent"`
	Panel   []string `toml:"panel"`
	Combine string   `toml:"combine"`
	Paths   []string `toml:"paths"`
	Trigger string   `toml:"trigger"`
	Carry   bool     `toml:"carry"`
}

// Judges returns the bindings that judge the gate: its agent, or its panel.
func (g Gate) Judges() []string {
	if len(g.Panel) > 0 {
		return g.Panel
	}
	if g.Agent != "" {
		return []string{g.Agent}
	}
	return nil
}

// Transition is one explicit move with the gate groups it runs, in order.
type Transition struct {
	From   []string `toml:"from"`
	To     string   `toml:"to"`
	Groups []Group  `toml:"groups"`
}

// Group is an ordered set of gates run sequentially (fail-fast) or in parallel.
type Group struct {
	Mode  string   `toml:"mode"`
	Gates []string `toml:"gates"`
}

// DecodeStrict decodes a TOML file into v and refuses any key v does not
// declare, naming it.
func DecodeStrict(path string, v any) error {
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return fmt.Errorf("%s: unknown key %s", path, strings.Join(keys, ", "))
	}
	return nil
}

// Parse reads one workflow file. The name defaults to the file's stem.
func Parse(path string) (*Workflow, error) {
	var w Workflow
	if err := DecodeStrict(path, &w); err != nil {
		return nil, err
	}
	if w.Name == "" {
		w.Name = strings.TrimSuffix(filepath.Base(path), ".toml")
	}
	w.File = path
	for i, t := range w.Transitions {
		for j, g := range t.Groups {
			if g.Mode == "" {
				w.Transitions[i].Groups[j].Mode = Sequential
			}
		}
	}
	for name, g := range w.Gates {
		if len(g.Panel) > 0 && g.Combine == "" {
			g.Combine = CombineAll
			w.Gates[name] = g
		}
	}
	return &w, nil
}

// Load reads every <dir>/*.toml. Two files naming the same workflow are refused.
func Load(dir string) (map[string]*Workflow, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := map[string]*Workflow{}
	var errs []error
	for _, p := range paths {
		w, err := Parse(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if prev, dup := out[w.Name]; dup {
			errs = append(errs, fmt.Errorf("%s: workflow %q is also defined in %s", p, w.Name, prev.File))
			continue
		}
		out[w.Name] = w
	}
	return out, errors.Join(errs...)
}

// StartState returns the workflow's start state.
func (w *Workflow) StartState() string {
	for _, name := range w.stateNames() {
		if w.States[name].Start {
			return name
		}
	}
	return ""
}

func (w *Workflow) stateNames() []string {
	names := make([]string, 0, len(w.States))
	for n := range w.States {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// IsTerminal reports whether s is a terminal state of the workflow.
func (w *Workflow) IsTerminal(s string) bool {
	st, ok := w.States[s]
	return ok && st.Terminal
}

// open reports whether s is a state "*" stands for: neither terminal nor park.
func (w *Workflow) open(s string) bool {
	st, ok := w.States[s]
	return ok && !st.Terminal && !st.Park
}

// Find returns the transition that moves an item from `from` to `to`. prior is
// the state a parked item was parked from, used to resolve "@prior". A
// transition naming from explicitly wins over one written with "*".
func (w *Workflow) Find(from, to, prior string) (*Transition, error) {
	var wild *Transition
	for i := range w.Transitions {
		t := &w.Transitions[i]
		target := t.To
		if target == Prior {
			target = prior
		}
		if target != to {
			continue
		}
		if slices.Contains(t.From, from) {
			return t, nil
		}
		if wild == nil && slices.Contains(t.From, AnyState) && w.open(from) {
			wild = t
		}
	}
	if wild != nil {
		return wild, nil
	}
	return nil, fmt.Errorf("workflow %s: no transition from %q to %q", w.Name, from, to)
}

// Targets lists the states reachable in one move from `from`.
func (w *Workflow) Targets(from, prior string) []string {
	var out []string
	for _, t := range w.Transitions {
		if !slices.Contains(t.From, from) && !(slices.Contains(t.From, AnyState) && w.open(from)) {
			continue
		}
		to := t.To
		if to == Prior {
			to = prior
		}
		if to != "" && !slices.Contains(out, to) {
			out = append(out, to)
		}
	}
	return out
}
