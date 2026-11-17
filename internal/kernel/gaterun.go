package kernel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

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
	prior := GateRuns(events)
	run := gate.Run{
		Item: it, From: from, To: to, Transition: tr, Workflow: w,
		Skills: k.Skills, Agents: k.Agents, Dispatcher: k.Dispatcher,
		Dir: k.Dir, Base: base, Changed: changed, Prior: prior,
		ReviewerDiffers: k.Settings.Rules.ReviewerDiffersFromPerformer,
		Review:          k.Settings.Review,
		CheckTimeout:    k.CheckTimeout,
		Coverage:        func(paths []string) (string, string, error) { return k.coverage(it, events, sp, paths) },
		Diff:            func(paths []string) (string, error) { return k.diffSpan(sp, paths) },
		Remote:          k.RemoteChecks,
		Logs:            k.Logs,
	}
	if k.Git != nil {
		run.Span = k.runSpan(events, sp)
	}
	if remotePlaced(w, tr) {
		run.Head, run.HeadErr = k.pushedHead(events, sp)
	}
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

func GateRuns(events []ledger.Event) []gate.Result {
	var out []gate.Result
	for _, e := range events {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil {
			out = append(out, r)
		}
	}
	for i := 0; i < len(out); {
		j := i + 1
		if out[i].Attempt != "" {
			for j < len(out) && out[j].Attempt == out[i].Attempt {
				j++
			}
		}
		run := out[i:j]
		if run[0].Attempt != "" && !slices.ContainsFunc(run, func(r gate.Result) bool { return r.Seq <= 0 }) {
			slices.SortStableFunc(run, func(a, b gate.Result) int { return a.Seq - b.Seq })
		}
		i = j
	}
	return out
}

type recorder struct {
	k                  *Kernel
	id, actor, key, at string
	mu                 sync.Mutex
	err                error
}

func (rc *recorder) record(kind string, seq int, payload any) bool {
	if _, err := rc.k.append(rc.id, kind, rc.actor, payload); err == nil {
		return true
	} else if rc.err == nil {
		rc.err = err
	}
	events, err := rc.k.Ledger.Read(rc.id)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Kind != kind {
			continue
		}
		var p struct {
			Attempt string `json:"attempt"`
			Seq     int    `json:"seq"`
		}
		if e.Decode(&p) == nil && p.Attempt == rc.at && p.Seq == seq {
			return true
		}
	}
	return false
}

func (rc *recorder) start(seq int, name string) error {
	rc.mu.Lock()
	if rc.err != nil {
		err := rc.err
		rc.mu.Unlock()
		return err
	}
	on := rc.record(gate.KindStart, seq, gate.Start{Transition: rc.key, Gate: name, Attempt: rc.at, Seq: seq})
	err := rc.err
	rc.mu.Unlock()
	if !on {
		return err
	}
	rc.progress()
	return nil
}

func (rc *recorder) finish(seq int, res gate.Result) {
	rc.mu.Lock()
	res.Attempt, res.Seq = rc.at, seq
	on := rc.record(gate.KindRun, seq, res)
	rc.mu.Unlock()
	if on {
		rc.progress()
	}
}

func (rc *recorder) progress() {
	if rc.k.Progress != nil {
		rc.k.Progress(rc.id)
	}
}

func (rc *recorder) failed() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.err
}

type GateDryRequest struct {
	Workflow, Gate, Item string
	From, To             string
	ShowInput            bool
}

type GateDryResult struct {
	Item, Workflow, Gate, From, To string
	Result                         *gate.Result
	Input                          *gate.Input
}

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
	if req.ShowInput {
		in, err := run.Input(req.Gate)
		if err != nil {
			return out, err
		}
		out.Input = &in
		return out, nil
	}
	unmark, err := k.markDryRun()
	if err != nil {
		return out, err
	}
	defer unmark()
	res := run.Gate(ctx, req.Gate)
	out.Result = &res
	return out, nil
}

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

func namesGate(tr *workflow.Transition, name string) bool {
	for _, g := range tr.Groups {
		if slices.Contains(g.Gates, name) {
			return true
		}
	}
	return false
}
