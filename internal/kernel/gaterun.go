package kernel

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// gateRun assembles one presentation of tr for it, from `from` to `to`: the
// span and its stale-base refusal, the changed paths, the item's earlier gate
// results, the pushed head when a gate is placed on a remote, and the record
// folder when a gate is judged. It is the one assembler a transition and a dry
// run share, so both hand a gate the same input. done removes the record
// folder; the caller defers it until the gates have run.
func (k *Kernel) gateRun(it item.Item, events []ledger.Event, w *workflow.Workflow, tr *workflow.Transition, from, to string) (gate.Run, span, func(), error) {
	done := func() {}
	sp := span{}
	if k.Git != nil {
		sp = k.spanOf(it, events)
	}
	if len(tr.Groups) > 0 {
		if stale := k.staleBase(it, sp); stale != nil {
			return gate.Run{}, sp, done, stale
		}
	}
	base := sp.base
	var changed []string
	if k.Git != nil && base != "" && len(tr.Groups) > 0 {
		var err error
		if changed, err = k.changed(sp); err != nil {
			return gate.Run{}, sp, done, err
		}
	}
	var prior []gate.Result
	for _, e := range events {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil {
			prior = append(prior, r)
		}
	}
	run := gate.Run{
		Item: it, From: from, To: to, Transition: tr, Workflow: w,
		Skills: k.Skills, Agents: k.Agents, Dispatcher: k.Dispatcher,
		Dir: k.Dir, Base: base, Changed: changed, Prior: prior,
		ReviewerDiffers: k.Settings.Rules.ReviewerDiffersFromPerformer,
		CheckTimeout:    k.CheckTimeout,
		Coverage:        func(paths []string) (string, string, error) { return k.coverage(it, events, sp, paths) },
		Diff:            func(paths []string) (string, error) { return k.diffSpan(sp, paths) },
		Remote:          k.RemoteChecks,
		Logs:            k.Logs,
	}
	if k.Git != nil {
		run.Span = k.runSpan(events, sp)
	}
	// A check placed on a remote judges the pushed head, not this tree.
	if remotePlaced(w, tr) {
		run.Head, run.HeadErr = k.pushedHead(events, sp)
	}
	// A judge reads the item's record; checks need none. The folder is
	// written after the changed paths are computed, and is gitignored.
	if k.judged(w, tr) {
		rec, err := k.record(it, events, true)
		if err != nil {
			return gate.Run{}, sp, done, err
		}
		done = func() { rec.Close() }
		run.Record = rec.Section
	}
	return run, sp, done, nil
}

// GateDryRequest names one gate of one workflow and the item to run it
// against. From defaults to the item's state; To to the one transition from
// From that names the gate. ShowInput reports what the gate would receive
// instead of running it.
type GateDryRequest struct {
	Workflow, Gate, Item string
	From, To             string
	ShowInput            bool
}

// GateDryResult is a dry run's outcome: the gate's result, or with ShowInput
// its input. It is what the caller prints, never recorded.
type GateDryResult struct {
	Item, Workflow, Gate, From, To string
	Result                         *gate.Result
	Input                          *gate.Input
}

// GateDry runs one configured gate against an item exactly as presenting its
// transition would, and records nothing: no gate.run, no hold, no enqueue.
// The record folder a judge reads is removed when it returns.
func (k *Kernel) GateDry(ctx context.Context, req GateDryRequest) (GateDryResult, error) {
	out := GateDryResult{Item: req.Item, Workflow: req.Workflow, Gate: req.Gate}
	it, events, err := k.load(req.Item)
	if err != nil {
		return out, err
	}
	w, ok := k.Workflows[req.Workflow]
	if !ok {
		return out, fmt.Errorf("no workflow %q; configured: %s", req.Workflow, strings.Join(sortedKeys(k.Workflows), ", "))
	}
	if _, ok := w.Gates[req.Gate]; !ok {
		return out, fmt.Errorf("workflow %s has no gate %q; configured: %s", w.Name, req.Gate, strings.Join(sortedKeys(w.Gates), ", "))
	}
	from := req.From
	if from == "" {
		from = it.Status
	}
	if _, ok := w.States[from]; !ok {
		return out, fmt.Errorf("%q is not a state of workflow %s (the item is at %q; name one with --from)", from, w.Name, it.Status)
	}
	prior := item.PriorStatus(events)
	to, tr, err := dryTransition(w, req.Gate, from, req.To, prior)
	if err != nil {
		return out, err
	}
	out.From, out.To = from, to
	run, _, done, err := k.gateRun(it, events, w, tr, from, to)
	if err != nil {
		return out, err
	}
	defer done()
	run.DryRun = true
	if req.ShowInput {
		in, err := run.Input(req.Gate)
		if err != nil {
			return out, err
		}
		out.Input = &in
		return out, nil
	}
	res := run.Gate(ctx, req.Gate)
	out.Result = &res
	return out, nil
}

// dryTransition is the transition from `from` a dry run of gate uses: the one
// to `to` when given, which must name the gate, else the only one from `from`
// that names it.
func dryTransition(w *workflow.Workflow, gateName, from, to, prior string) (string, *workflow.Transition, error) {
	if to != "" {
		tr, err := w.Find(from, to, prior)
		if err != nil {
			return "", nil, err
		}
		if !namesGate(tr, gateName) {
			return "", nil, fmt.Errorf("workflow %s: the transition %s -> %s does not run gate %s", w.Name, from, to, gateName)
		}
		return to, tr, nil
	}
	var targets []string
	var found *workflow.Transition
	for _, s := range sortedKeys(w.States) {
		tr, err := w.Find(from, s, prior)
		if err != nil || !namesGate(tr, gateName) {
			continue
		}
		targets = append(targets, s)
		found = tr
	}
	switch len(targets) {
	case 1:
		return targets[0], found, nil
	case 0:
		return "", nil, fmt.Errorf("workflow %s: no transition from %q runs gate %s (name the state with --from)", w.Name, from, gateName)
	default:
		return "", nil, fmt.Errorf("workflow %s: gate %s runs on more than one transition from %q, to %s (choose one with --to)", w.Name, gateName, from, strings.Join(targets, ", "))
	}
}

// namesGate reports whether any group of tr runs the gate.
func namesGate(tr *workflow.Transition, name string) bool {
	for _, g := range tr.Groups {
		if slices.Contains(g.Gates, name) {
			return true
		}
	}
	return false
}
