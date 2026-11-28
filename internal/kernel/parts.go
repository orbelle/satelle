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

type TargetMismatch struct {
	Item, Workflow, Part, Target string
}

func (e *TargetMismatch) Error() string {
	return fmt.Sprintf("item %s is on workflow %s of repository %s but targets %s; a repository's workflow serves only that repository's items",
		e.Item, reporeg.Literal(e.Workflow), reporeg.Literal(e.Part), reporeg.Literal(e.Target))
}

func ServesTarget(w *workflow.Workflow, it item.Item) error {
	if w.Part == "" || w.Part == it.Target() {
		return nil
	}
	return &TargetMismatch{Item: it.ID, Workflow: w.Name, Part: w.Part, Target: it.Target()}
}

func ItemWorkflow(ws map[string]*workflow.Workflow, it item.Item) (*workflow.Workflow, error) {
	w, ok := ws[it.Workflow]
	if !ok {
		return nil, fmt.Errorf("item %s names workflow %q, which does not exist", it.ID, it.Workflow)
	}
	if err := ServesTarget(w, it); err != nil {
		return nil, err
	}
	return w, nil
}

func (k *Kernel) ItemWorkflow(it item.Item) (*workflow.Workflow, error) {
	return ItemWorkflow(k.Workflows, it)
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

func loadParts(root string, reg reporeg.Registry) (map[string]workflow.Rules, map[string]*workflow.Workflow, []error) {
	dir := filepath.Join(root, PartsDir)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, []error{partErr("%s: cannot be read", PartsDir)}
	}
	if !info.IsDir() {
		return nil, nil, []error{partErr("%s: not a directory", PartsDir)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, []error{partErr("%s: cannot be read", PartsDir)}
	}
	rules := map[string]workflow.Rules{}
	wfs := map[string]*workflow.Workflow{}
	var errs []error
	for _, e := range entries {
		x := e.Name()
		switch {
		case !reporeg.ValidName(x):
			errs = append(errs, partErr("%s/%s: not a valid repository name", PartsDir, reporeg.Literal(x)))
			continue
		case x == reporeg.Self:
			errs = append(errs, partErr("%s/%s: self is the control repository; its workflows are the shared %s/", PartsDir, x, WorkflowsDir))
			continue
		case !reg.EntryOK(x):
			errs = append(errs, partErr("%s/%s: %s registers no valid repository of that name", PartsDir, x, reporeg.TrackedFile))
			continue
		}
		r, ws, perrs := loadPart(dir, x)
		errs = append(errs, perrs...)
		if r != nil {
			rules[x] = *r
		}
		for _, n := range sortedKeys(ws) {
			wfs[n] = ws[n]
		}
	}
	if len(rules) == 0 {
		rules = nil
	}
	return rules, wfs, errs
}

func loadPart(dir, x string) (*workflow.Rules, map[string]*workflow.Workflow, []error) {
	rel := PartsDir + "/" + x
	partDir := filepath.Join(dir, x)
	info, err := os.Lstat(partDir)
	if err != nil {
		return nil, nil, []error{partErr("%s: cannot be read", rel)}
	}
	if !info.IsDir() {
		return nil, nil, []error{partErr("%s: not a directory", rel)}
	}
	var errs []error
	rules, err := loadPartRules(partDir, rel)
	if err != nil {
		errs = append(errs, err)
	}
	wfs, werrs := loadPartWorkflows(partDir, rel, x)
	return rules, wfs, append(errs, werrs...)
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

func mergeParts(c *Config, wfs map[string]*workflow.Workflow) []error {
	var errs []error
	for _, n := range sortedKeys(wfs) {
		w := wfs[n]
		if prev, dup := c.Workflows[n]; dup {
			errs = append(errs, partErr("%s/%s/%s: workflow %s is also defined in %s", PartsDir, w.Part, WorkflowsDir, n, prev.File))
			continue
		}
		c.Workflows[n] = w
	}
	return errs
}

func (c *Config) partProblems() []error {
	var errs []error
	for _, x := range sortedKeys(c.Parts) {
		r := c.Parts[x]
		file := PartsDir + "/" + x + "/" + SettingsFile
		exists := func(n string) bool {
			w, ok := c.Workflows[x+"/"+n]
			return ok && w.Part == x
		}
		if r.Default != "" && !exists(r.Default) {
			errs = append(errs, partErr("%s: workflows: default workflow %s is not defined under %s/%s/%s/", file, reporeg.Literal(r.Default), PartsDir, x, WorkflowsDir))
		}
		for _, e := range r.RuleProblems(exists) {
			errs = append(errs, partErr("%s: %v", file, e))
		}
	}
	return errs
}
