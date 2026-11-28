package kernel

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type PartWarning struct {
	Dir, Reason string
}

func (w PartWarning) String() string {
	return fmt.Sprintf("%s/%s: not loaded: %s", PartsDir, reporeg.Literal(w.Dir), w.Reason)
}

type Part struct {
	Name      string
	Rules     workflow.Rules
	Workflows map[string]*workflow.Workflow
	Skills    map[string]workflow.Skill
	Problems  []error
}

type PartUnresolved struct {
	Item, Repo string
	Problems   []error
}

func (e *PartUnresolved) Error() string {
	msgs := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		msgs[i] = p.Error()
	}
	return fmt.Sprintf("item %s targets repository %s, whose part .satelle/%s/%s/ does not resolve, so its stories are refused until it does: %s",
		e.Item, reporeg.Literal(e.Repo), PartsDir, reporeg.Literal(e.Repo), strings.Join(msgs, "; "))
}

func (e *PartUnresolved) Unplaceable() bool { return true }

type TargetMismatch struct {
	Item, Workflow, Part, Target string
}

func (e *TargetMismatch) Error() string {
	return fmt.Sprintf("item %s is on workflow %s of repository %s but targets %s; a repository's workflow serves only that repository's items",
		e.Item, reporeg.Literal(e.Workflow), reporeg.Literal(e.Part), reporeg.Literal(e.Target))
}

func Refused(err error) bool {
	var u *PartUnresolved
	var m *TargetMismatch
	return errors.As(err, &u) || errors.As(err, &m)
}

type Resolver struct {
	Workflows  map[string]*workflow.Workflow
	Unresolved map[string][]error
}

func (r Resolver) Refusal(it item.Item) error {
	t := it.Target()
	if ps := r.Unresolved[t]; len(ps) > 0 {
		return &PartUnresolved{Item: it.ID, Repo: t, Problems: ps}
	}
	return nil
}

func (r Resolver) Serves(w *workflow.Workflow, it item.Item) error {
	if err := r.Refusal(it); err != nil {
		return err
	}
	if w.Part == "" || w.Part == it.Target() {
		return nil
	}
	return &TargetMismatch{Item: it.ID, Workflow: w.Name, Part: w.Part, Target: it.Target()}
}

func (r Resolver) ItemWorkflow(it item.Item) (*workflow.Workflow, error) {
	if err := r.Refusal(it); err != nil {
		return nil, err
	}
	w, ok := r.Workflows[it.Workflow]
	if !ok {
		return nil, fmt.Errorf("item %s names workflow %q, which does not exist", it.ID, it.Workflow)
	}
	if err := r.Serves(w, it); err != nil {
		return nil, err
	}
	return w, nil
}

func (k *Kernel) Resolver() Resolver {
	return Resolver{Workflows: k.Workflows, Unresolved: k.Unresolved}
}

func (c *Config) Resolver() Resolver {
	if c == nil {
		return Resolver{}
	}
	return Resolver{Workflows: c.Workflows, Unresolved: c.Unresolved()}
}

func (k *Kernel) ItemWorkflow(it item.Item) (*workflow.Workflow, error) {
	return k.Resolver().ItemWorkflow(it)
}

func (k *Kernel) servedWorkflow(it item.Item) *workflow.Workflow {
	w, _ := k.ItemWorkflow(it)
	return w
}

func (k *Kernel) partName(it item.Item) string {
	t := it.Target()
	if t == reporeg.Self {
		return ""
	}
	rules, ok := k.PartRules[t]
	if !ok {
		return ""
	}
	if n := rules.Select(it); n != "" {
		return t + "/" + n
	}
	return ""
}

func (c *Config) PartRules() map[string]workflow.Rules {
	var out map[string]workflow.Rules
	for x, p := range c.Parts {
		if len(p.Problems) > 0 {
			continue
		}
		if out == nil {
			out = map[string]workflow.Rules{}
		}
		out[x] = p.Rules
	}
	return out
}

func (c *Config) Unresolved() map[string][]error {
	var out map[string][]error
	for x, p := range c.Parts {
		if len(p.Problems) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]error{}
		}
		out[x] = p.Problems
	}
	return out
}

func partSafe(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if reporeg.HasUnsafe(msg) || !utf8.ValidString(msg) {
		return errors.New(reporeg.Literal(msg))
	}
	return err
}

func partErr(format string, a ...any) error {
	return partSafe(fmt.Errorf(format, a...))
}

func partSkip(reg reporeg.Registry, x string) string {
	switch {
	case !reporeg.ValidName(x):
		return "not a valid repository name"
	case x == reporeg.Self:
		return "self is the control repository; its workflows are the shared " + WorkflowsDir + "/"
	case !reg.EntryOK(x):
		return reporeg.TrackedFile + " registers no valid repository of that name"
	}
	return ""
}

func (c *Config) loadParts() error {
	dir := filepath.Join(c.Root, PartsDir)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return partErr("%s: cannot be read", PartsDir)
	}
	if !info.IsDir() {
		return partErr("%s: not a directory", PartsDir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return partErr("%s: cannot be read", PartsDir)
	}
	for _, e := range entries {
		x := e.Name()
		if reason := partSkip(c.Repos, x); reason != "" {
			c.PartWarnings = append(c.PartWarnings, PartWarning{Dir: x, Reason: reason})
			continue
		}
		c.loadPart(dir, x)
	}
	for _, x := range sortedKeys(c.Parts) {
		p := c.Parts[x]
		if len(p.Problems) > 0 {
			continue
		}
		for _, n := range sortedKeys(p.Workflows) {
			c.Workflows[n] = p.Workflows[n]
		}
	}
	return nil
}

func (c *Config) loadPart(dir, x string) {
	p := &Part{Name: x, Workflows: map[string]*workflow.Workflow{}}
	if c.Parts == nil {
		c.Parts = map[string]*Part{}
	}
	c.Parts[x] = p
	rel := PartsDir + "/" + x
	partDir := filepath.Join(dir, x)
	info, err := os.Lstat(partDir)
	if err != nil {
		p.Problems = append(p.Problems, partErr("%s: cannot be read", rel))
		return
	}
	if !info.IsDir() {
		p.Problems = append(p.Problems, partErr("%s: not a directory", rel))
		return
	}
	c.loadPartSkills(p, partDir)
	rules, err := loadPartRules(partDir, rel)
	if err != nil {
		p.Problems = append(p.Problems, err)
	} else if rules != nil {
		p.Rules = *rules
	}
	wfs, errs := loadPartWorkflows(partDir, rel, x)
	p.Problems = append(p.Problems, errs...)
	for _, n := range sortedKeys(wfs) {
		w := wfs[n]
		if prev, dup := c.Workflows[n]; dup {
			p.Problems = append(p.Problems, partErr("%s/%s: workflow %s is also defined in %s", rel, WorkflowsDir, n, prev.File))
			continue
		}
		p.Workflows[n] = w
	}
	p.Problems = append(p.Problems, c.resolvePart(p)...)
}

func (c *Config) resolvePart(p *Part) []error {
	x := p.Name
	file := PartsDir + "/" + x + "/" + SettingsFile
	exists := func(n string) bool { _, ok := p.Workflows[x+"/"+n]; return ok }
	var errs []error
	if d := p.Rules.Default; d != "" && !exists(d) {
		errs = append(errs, partErr("%s: workflows: default workflow %s is not defined under %s/%s/%s/", file, reporeg.Literal(d), PartsDir, x, WorkflowsDir))
	}
	for _, e := range p.Rules.RuleProblems(exists) {
		errs = append(errs, partErr("%s: %v", file, e))
	}
	for _, n := range sortedKeys(p.Workflows) {
		for _, e := range c.workflowProblems(p.Workflows[n]) {
			errs = append(errs, partErr("%s/%s: %v", PartsDir, x, e))
		}
	}
	return errs
}

func loadPartRules(partDir, rel string) (*workflow.Rules, error) {
	file := rel + "/" + SettingsFile
	data, err := reporeg.ReadRegular(filepath.Join(partDir, SettingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, partErr("%s: %v", file, err)
	}
	var st struct {
		Workflows workflow.Rules `toml:"workflows"`
	}
	unknown, err := workflow.DecodeData(file, data, &st)
	if err != nil {
		return nil, partSafe(err)
	}
	var keys []string
	for _, u := range unknown {
		if u.Table == "workflows" || strings.HasPrefix(u.Table, "workflows.") {
			keys = append(keys, strictKeyName(u))
		}
	}
	if len(keys) > 0 {
		return nil, partErr("%s: unknown key %s", file, strings.Join(keys, ", "))
	}
	return &st.Workflows, nil
}

func loadPartWorkflows(partDir, rel, x string) (map[string]*workflow.Workflow, []error) {
	wrel := rel + "/" + WorkflowsDir
	wdir := filepath.Join(partDir, WorkflowsDir)
	info, err := os.Lstat(wdir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{partErr("%s: cannot be read", wrel)}
	}
	if !info.IsDir() {
		return nil, []error{partErr("%s: not a directory", wrel)}
	}
	entries, err := os.ReadDir(wdir)
	if err != nil {
		return nil, []error{partErr("%s: cannot be read", wrel)}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".toml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := map[string]*workflow.Workflow{}
	var errs []error
	for _, fname := range names {
		stem := strings.TrimSuffix(fname, ".toml")
		if !reporeg.ValidName(stem) {
			errs = append(errs, partErr("%s: file %s is not a valid workflow file name", wrel, reporeg.Literal(fname)))
			continue
		}
		path := filepath.Join(wdir, fname)
		frel := wrel + "/" + fname
		data, err := reporeg.ReadRegular(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				err = errors.New("cannot be read")
			}
			errs = append(errs, partErr("%s: %v", frel, err))
			continue
		}
		w, err := workflow.ParseData(frel, data)
		if err != nil {
			errs = append(errs, partSafe(err))
			continue
		}
		if !reporeg.ValidName(w.Name) {
			errs = append(errs, partErr("%s: workflow name %s is not a valid name", wrel, reporeg.Literal(w.Name)))
			continue
		}
		w.File, w.Part, w.Source = path, x, data
		w.Name = x + "/" + w.Name
		if prev, dup := out[w.Name]; dup {
			errs = append(errs, partErr("%s: workflow %s is also defined in %s", frel, w.Name, prev.File))
			continue
		}
		out[w.Name] = w
	}
	return out, errs
}
