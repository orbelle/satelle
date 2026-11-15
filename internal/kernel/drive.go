package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const MaxDriveSteps = 100

const (
	VerdictAccept = "accept"
	VerdictReject = "reject"
)

type DriveStep struct {
	State     string `json:"state"`
	Performed bool   `json:"performed"`
	To        string `json:"to,omitempty"`
	Moved     bool   `json:"moved"`
}

type DriveResult struct {
	Item    item.Item   `json:"item"`
	Steps   []DriveStep `json:"steps"`
	Verdict string      `json:"verdict"`
	Notes   string      `json:"notes"`
}

func (k *Kernel) Drive(ctx context.Context, id, actor string) (DriveResult, error) {
	return k.DriveSteps(ctx, id, actor, 0)
}

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
		next := nextTarget(w, it.Status, item.PriorStatus(events))
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

func nextTarget(w *workflow.Workflow, status, prior string) string {
	for _, t := range w.Targets(status, prior) {
		if s, ok := w.States[t]; ok && !s.Park {
			return t
		}
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
