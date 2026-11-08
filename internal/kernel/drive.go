package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

// MaxDriveSteps bounds a headless drive: a workflow with a cycle stops here.
const MaxDriveSteps = 100

// Verdicts a drive ends with.
const (
	VerdictAccept = "accept"
	VerdictReject = "reject"
)

// DriveStep is one state a drive passed through.
type DriveStep struct {
	State     string `json:"state"`
	Performed bool   `json:"performed"`
	To        string `json:"to,omitempty"`
	Moved     bool   `json:"moved"`
}

// DriveResult is where a drive stopped and why.
type DriveResult struct {
	Item    item.Item   `json:"item"`
	Steps   []DriveStep `json:"steps"`
	Verdict string      `json:"verdict"` // accept when the item reached a terminal state
	Notes   string      `json:"notes"`   // why it stopped
}

// Drive walks an item headlessly along its workflow: perform the current
// state's performer, then present the next transition (the first, in the
// workflow's order, whose target is not a park state). It stops at a terminal
// or park state, at a rejected transition, at a performer that fails or is the
// interactive session, or at a state with nowhere to go. An item that reached
// a terminal state is accepted; any other stop is a reject with its reason.
func (k *Kernel) Drive(ctx context.Context, id, actor string) (DriveResult, error) {
	return k.DriveSteps(ctx, id, actor, 0)
}

// DriveSteps is Drive bounded to limit steps. limit 0 is Drive: at most
// MaxDriveSteps, and reaching that bound is a reject. With limit > 0, limit steps
// that all moved conclude accept, noting the last move; every other stop is
// Drive's.
func (k *Kernel) DriveSteps(ctx context.Context, id, actor string, limit int) (DriveResult, error) {
	bounded := limit > 0
	if !bounded {
		limit = MaxDriveSteps
	}
	var res DriveResult
	stop := func(it item.Item, verdict, notes string) (DriveResult, error) {
		res.Item, res.Verdict, res.Notes = it, verdict, notes
		return res, nil
	}
	for range limit {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		it, events, err := k.load(id)
		if err != nil {
			return res, err
		}
		w, err := k.workflowOf(it)
		if err != nil {
			return res, err
		}
		st := w.States[it.Status]
		switch {
		case st.Terminal:
			return stop(it, VerdictAccept, fmt.Sprintf("reached %s", it.Status))
		case st.Park:
			return stop(it, VerdictReject, fmt.Sprintf("parked at %s", it.Status))
		}
		step := DriveStep{State: it.Status}
		if typ, ok := st.Operated(); ok {
			res.Steps = append(res.Steps, step)
			return stop(it, VerdictReject, fmt.Sprintf("state %s is the operator's: it needs the operator's %s record", it.Status, typ))
		}
		if p := st.Agent(); p != nil {
			if agent.IsSession(k.Agents[p.Agent]) {
				res.Steps = append(res.Steps, step)
				return stop(it, VerdictReject, fmt.Sprintf("state %s is performed by the interactive session", it.Status))
			}
			if _, err := k.Perform(ctx, id, actor); err != nil {
				res.Steps = append(res.Steps, step)
				return stop(it, VerdictReject, fmt.Sprintf("performing %s failed: %v", it.Status, err))
			}
			step.Performed = true
		}
		next := ""
		for _, t := range w.Targets(it.Status, item.PriorStatus(events)) {
			if s, ok := w.States[t]; ok && !s.Park {
				next = t
				break
			}
		}
		if next == "" {
			res.Steps = append(res.Steps, step)
			return stop(it, VerdictReject, fmt.Sprintf("no transition leaves %s", it.Status))
		}
		step.To = next
		tr, err := k.Transition(ctx, id, next, actor)
		if err != nil {
			return res, err
		}
		step.Moved = tr.Moved
		res.Steps = append(res.Steps, step)
		if tr.Held != nil {
			return stop(tr.Item, VerdictReject, fmt.Sprintf("%s held at %s after %d rounds", it.Status, tr.Item.Status, tr.Held.Rounds))
		}
		if !tr.Moved {
			var why []string
			for _, g := range tr.Rejects() {
				why = append(why, g.Gate+": "+firstLine(g.Notes))
			}
			for _, g := range tr.Errors() {
				why = append(why, g.Gate+": "+gate.Error+": "+firstLine(g.Notes))
			}
			return stop(tr.Item, VerdictReject, fmt.Sprintf("%s → %s rejected (%s)", it.Status, next, strings.Join(why, "; ")))
		}
		if bounded && len(res.Steps) == limit {
			return stop(tr.Item, VerdictAccept, fmt.Sprintf("moved %s → %s; stopped after %d step(s)", it.Status, next, limit))
		}
	}
	it, err := k.Get(id)
	if err != nil {
		return res, err
	}
	return stop(it, VerdictReject, fmt.Sprintf("stopped after %d steps", MaxDriveSteps))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
