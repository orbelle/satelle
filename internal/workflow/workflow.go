package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	AnyState    = "*"
	Prior       = "@prior"
	Sequential  = "sequential"
	Parallel    = "parallel"
	CombineAll  = "all"
	CombineAny  = "any"
	CombineMost = "majority"
)

type Workflow struct {
	Name        string           `toml:"name"`
	Panel       *string          `toml:"panel"`
	States      map[string]State `toml:"states"`
	Gates       map[string]Gate  `toml:"gates"`
	Transitions []Transition     `toml:"transition"`
	File        string           `toml:"-"`
}

func (w *Workflow) PanelName() string {
	if w.Panel == nil {
		return ""
	}
	return strings.TrimSpace(*w.Panel)
}

type State struct {
	Start     bool       `toml:"start"`
	Terminal  bool       `toml:"terminal"`
	Park      bool       `toml:"park"`
	Resumes   bool       `toml:"resumes"`
	Performer *Performer `toml:"performer"`
	Enqueue   []string   `toml:"enqueue"`
	Waits     string     `toml:"waits"`
	Hold      *Hold      `toml:"hold"`
	Edits     string     `toml:"edits"`
}

const (
	EditsTree        = "tree"
	EditsAttachments = "attachments"
)

func (s State) EditScope() string {
	if s.Agent() == nil {
		return ""
	}
	if s.Edits == "" {
		return EditsTree
	}
	return s.Edits
}

type Hold struct {
	Rounds int    `toml:"rounds"`
	To     string `toml:"to"`
}

type Performer struct {
	Agent    string `toml:"agent"`
	Skill    string `toml:"skill"`
	Operator string `toml:"operator"`
}

func (s State) Operated() (string, bool) {
	if s.Performer == nil || s.Performer.Operator == "" {
		return "", false
	}
	return s.Performer.Operator, true
}

func (s State) Agent() *Performer {
	if s.Performer == nil || s.Performer.Operator != "" {
		return nil
	}
	return s.Performer
}

type Gate struct {
	Skill        string   `toml:"skill"`
	Agent        string   `toml:"agent"`
	Panel        []string `toml:"panel"`
	Combine      string   `toml:"combine"`
	Paths        []string `toml:"paths"`
	Trigger      string   `toml:"trigger"`
	Carry        bool     `toml:"carry"`
	After        []string `toml:"after"`
	Timeout      string   `toml:"timeout"`
	DiffMaxBytes *int     `toml:"diff_max_bytes"`
	Placement    string   `toml:"placement"`
}

func (g Gate) DiffBound() (int, bool) {
	if g.DiffMaxBytes == nil {
		return 0, false
	}
	return *g.DiffMaxBytes, true
}

func (g Gate) Bound() (time.Duration, error) {
	if g.Timeout == "" {
		return 0, nil
	}
	return time.ParseDuration(g.Timeout)
}

func (g Gate) Judges() []string {
	if len(g.Panel) > 0 {
		return g.Panel
	}
	if g.Agent != "" {
		return []string{g.Agent}
	}
	return nil
}

type Transition struct {
	From   []string `toml:"from"`
	To     string   `toml:"to"`
	Groups []Group  `toml:"groups"`
}

type Group struct {
	Mode  string   `toml:"mode"`
	Gates []string `toml:"gates"`
}

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

func Parse(path string) (*Workflow, error) {
	var w Workflow
	if err := DecodeStrict(path, &w); err != nil {
		if hint := afterNotAList(path); hint != nil {
			return nil, hint
		}
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

func afterNotAList(path string) error {
	var raw struct {
		Gates map[string]map[string]any `toml:"gates"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil
	}
	names := make([]string, 0, len(raw.Gates))
	for name := range raw.Gates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, ok := raw.Gates[name]["after"]
		if !ok {
			continue
		}
		list, good := v.([]any)
		for _, e := range list {
			if _, isStr := e.(string); !isStr {
				good = false
			}
		}
		if good {
			continue
		}
		got, example := fmt.Sprint(v), `["<check skill>"]`
		if s, isStr := v.(string); isStr {
			got = fmt.Sprintf("the string %q", s)
			if s != "" {
				example = fmt.Sprintf("[%q]", s)
			}
		}
		return fmt.Errorf("%s: gates.%s.after must be a list of check skill names, not %s: write after = %s", path, name, got, example)
	}
	return nil
}

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

func (w *Workflow) IsTerminal(s string) bool {
	st, ok := w.States[s]
	return ok && st.Terminal
}

func (w *Workflow) open(s string) bool {
	st, ok := w.States[s]
	return ok && !st.Terminal && !st.Park
}

func MatchTransition(n int, edge func(i int) (from []string, to string), open func(state string) bool, from, to, prior string) int {
	wild := -1
	for i := range n {
		froms, target := edge(i)
		if target == Prior {
			target = prior
		}
		if target != to {
			continue
		}
		if slices.Contains(froms, from) {
			return i
		}
		if wild < 0 && slices.Contains(froms, AnyState) && open(from) {
			wild = i
		}
	}
	return wild
}

func (w *Workflow) Find(from, to, prior string) (*Transition, error) {
	i := MatchTransition(len(w.Transitions), func(i int) ([]string, string) { return w.Transitions[i].From, w.Transitions[i].To }, w.open, from, to, prior)
	if i < 0 {
		return nil, fmt.Errorf("workflow %s: no transition from %q to %q", w.Name, from, to)
	}
	return &w.Transitions[i], nil
}

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
