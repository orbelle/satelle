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
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func (k *Kernel) gateRun(it item.Item, events []ledger.Event, w *workflow.Workflow, tr *workflow.Transition, from, to string) (gate.Run, span, func(), error) {
	done := func() {}
	sp := span{}
	if len(tr.Groups) > 0 {
		if err := k.CheckTarget(it); err != nil {
			return gate.Run{}, sp, done, err
		}
	}
	wt, err := k.worktreeOf(it, events)
	if err != nil {
		if len(tr.Groups) > 0 {
			return gate.Run{}, sp, done, err
		}
		wt = worktree{}
	}
	if wt.git != nil {
		sp = k.spanOf(wt, it, events)
	}
	if len(tr.Groups) > 0 {
		if stale := k.staleBase(it, sp); stale != nil {
			return gate.Run{}, sp, done, stale
		}
	}
	base := sp.base
	var changed, untracked []string
	if sp.git != nil && base != "" && len(tr.Groups) > 0 {
		if changed, err = k.changed(sp); err != nil {
			return gate.Run{}, sp, done, err
		}
		if untracked, err = k.untracked(sp); err != nil {
			return gate.Run{}, sp, done, err
		}
	}
	prior := GateRuns(events)
	run := gate.Run{
		Item: it, From: from, To: to, Transition: tr, Workflow: w,
		Skills: k.Skills, Agents: k.Agents, Dispatcher: k.Dispatcher,
		Dir: k.Dir, Substrate: k.SubstrateDir(), Base: base, Changed: changed, Untracked: untracked, Prior: prior,
		Review:       k.Settings.Review,
		CheckTimeout: k.CheckTimeout,
		Coverage:     func(paths []string) (string, string, error) { return k.coverage(it, events, sp, paths) },
		Diff:         func(paths []string) (string, error) { return k.diffSpan(sp, paths) },
		Remote:       k.RemoteChecks,
		Logs:         k.Logs,
		Redact:       k.Redact,
		Guard:        k.Guard,
		Staged:       k.Staged,
	}
	if wt.target != "" && wt.target != reporeg.Self {
		run.Worktree = wt.dir
	}
	if sp.git != nil {
		run.Span = k.runSpan(events, sp)
	}
	if remotePlaced(w, tr) {
		run.Head, run.HeadErr = k.pushedHead(wt, it, events, sp)
	}
	if k.judged(w, tr) {
		var rec Record
		if run.Worktree != "" {
			rec, err = k.targetRecord(it, events, recordPointers, wt)
		} else {
			rec, err = k.record(it, events, recordPointers)
		}
		if err != nil {
			return gate.Run{}, sp, done, err
		}
		done = func() { rec.Close() }
		run.Record = rec.Section
		run.RecordDir, run.Scratch = rec.Dir, rec.Scratch
	}
	return run, sp, done, nil
}

func GateRuns(events []ledger.Event) []gate.Result {
	var out []gate.Result
	for _, e := range events {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil {
			r.At = e.At
			out = append(out, r)
		}
	}
	sortAttemptRuns(out, func(r gate.Result) (string, int) { return r.Attempt, r.Seq })
	return out
}

func sortAttemptRuns[T any](items []T, meta func(T) (attempt string, seq int)) {
	for i := 0; i < len(items); {
		at, _ := meta(items[i])
		j := i + 1
		if at != "" {
			for j < len(items) {
				nextAt, _ := meta(items[j])
				if nextAt != at {
					break
				}
				j++
			}
		}
		run := items[i:j]
		if at != "" && !slices.ContainsFunc(run, func(x T) bool { _, seq := meta(x); return seq <= 0 }) {
			slices.SortStableFunc(run, func(a, b T) int {
				_, sa := meta(a)
				_, sb := meta(b)
				return sa - sb
			})
		}
		i = j
	}
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
	res.ModelID = rc.k.modelID(res.Model)
	for i := range res.Members {
		res.Members[i].ModelID = rc.k.modelID(res.Members[i].Model)
	}
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
