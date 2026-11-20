package kernel

import (
	"context"
	"fmt"
	"sort"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const unclassified = "unclassified"

type attempt struct {
	ID      string
	Results []gate.Result
}

func attemptsLeaving(status string, events []ledger.Event) []attempt {
	var out []attempt
	for _, r := range GateRuns(item.SinceEntered(events)) {
		if r.Attempt == "" || gate.FromOf(r.Transition) != status {
			continue
		}
		if n := len(out); n == 0 || out[n-1].ID != r.Attempt {
			out = append(out, attempt{ID: r.Attempt})
		}
		out[len(out)-1].Results = append(out[len(out)-1].Results, r)
	}
	return out
}

func rounds(w *workflow.Workflow, attempts []attempt) []attempt {
	var out []attempt
	for _, a := range attempts {
		if len(a.Results) == 0 || w.States[gate.ToOf(a.Results[0].Transition)].Park {
			continue
		}
		for _, r := range a.Results {
			if r.Result == gate.Reject {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

type verdict struct {
	gate, binding, result, class, command, model string
	tokensIn, tokensOut                          int
	durationMS                                   int64
	measured                                     bool
}

func verdictsOf(a attempt) []verdict {
	var out []verdict
	for _, r := range a.Results {
		out = append(out, verdictsOfResult(r)...)
	}
	return out
}

func verdictsOfResult(r gate.Result) []verdict {
	if len(r.Members) == 0 {
		return []verdict{{gate: r.Gate, binding: r.Binding, result: r.Result, class: r.Class, command: r.Command, model: r.Model,
			tokensIn: r.TokensIn, tokensOut: r.TokensOut, durationMS: r.DurationMS, measured: true}}
	}
	out := make([]verdict, 0, len(r.Members))
	for _, m := range r.Members {
		out = append(out, verdict{gate: r.Gate, binding: m.Binding, result: m.Result, class: m.Class, command: m.Command, model: m.Model,
			tokensIn: m.TokensIn, tokensOut: m.TokensOut, durationMS: m.DurationMS, measured: m.DurationMS > 0})
	}
	return out
}

func holdOf(state string, counted []attempt) item.Hold {
	h := item.Hold{State: state, Rounds: len(counted), Dissent: []item.Dissent{}, Accepted: []string{}}
	type tally struct {
		d       item.Dissent
		classes map[string]int
		last    map[string]int
	}
	byKey := map[string]*tally{}
	var order []string
	for i, a := range counted {
		for _, v := range verdictsOf(a) {
			if v.result != gate.Reject {
				continue
			}
			key := item.VerdictKey(v.gate, v.binding)
			t, ok := byKey[key]
			if !ok {
				t = &tally{d: item.Dissent{Gate: v.gate, Binding: v.binding}, classes: map[string]int{}, last: map[string]int{}}
				byKey[key] = t
				order = append(order, key)
			}
			t.d.Rejected++
			if v.class != "" {
				t.classes[v.class]++
				t.last[v.class] = i
			}
		}
	}
	for _, key := range order {
		t := byKey[key]
		t.d.Class, t.d.ClassRounds = unclassified, t.d.Rejected
		best := -1
		for c, n := range t.classes {
			if n > best || (n == best && t.last[c] > t.last[t.d.Class]) {
				best, t.d.Class, t.d.ClassRounds = n, c, n
			}
		}
		h.Dissent = append(h.Dissent, t.d)
	}
	sort.SliceStable(h.Dissent, func(i, j int) bool {
		a, b := h.Dissent[i], h.Dissent[j]
		if a.Rejected != b.Rejected {
			return a.Rejected > b.Rejected
		}
		return a.Key() < b.Key()
	})
	if n := len(counted); n > 0 {
		for _, v := range verdictsOf(counted[n-1]) {
			if v.result == gate.Accept || v.result == gate.Carried {
				h.Accepted = append(h.Accepted, item.VerdictKey(v.gate, v.binding))
			}
		}
	}
	return h
}

func (k *Kernel) holdIfCapped(ctx context.Context, res TransitionResult, w *workflow.Workflow, actor string) (TransitionResult, error) {
	hc := w.States[res.From].Hold
	if hc == nil || w.States[res.To].Park {
		return res, nil
	}
	id := res.Item.ID
	_, events, err := k.load(id)
	if err != nil {
		return res, err
	}
	counted := rounds(w, attemptsLeaving(res.From, events))
	if len(counted) < hc.Rounds {
		return res, nil
	}
	h := holdOf(res.From, counted)
	moved, err := k.transition(ctx, id, hc.To, actor, &h)
	if err != nil {
		return res, fmt.Errorf("%s reached its cap of %d rejected rounds leaving %s, but holding it at %s failed: %w", id, hc.Rounds, res.From, hc.To, err)
	}
	if !moved.Moved {
		return res, fmt.Errorf("%s reached its cap of %d rejected rounds leaving %s, but the move to %s did not happen", id, hc.Rounds, res.From, hc.To)
	}
	res.Item, res.Held, res.Queued = moved.Item, &h, moved.Queued
	return res, nil
}

func (k *Kernel) HoldOf(id string) (*item.Hold, error) {
	it, events, err := k.load(id)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != item.KindTransitioned {
			continue
		}
		var m item.Move
		if err := events[i].Decode(&m); err != nil {
			return nil, err
		}
		if m.To == it.Status {
			return m.Hold, nil
		}
		return nil, nil
	}
	return nil, nil
}
