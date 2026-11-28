package kernel

import (
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const (
	OutcomePerformer = "performer"
	OutcomeGate      = "gate"
)

type ItemOutcome struct {
	Item     string           `json:"item"`
	Workflow string           `json:"workflow"`
	Status   string           `json:"status"`
	Terminal bool             `json:"terminal"`
	Rows     []ItemOutcomeRow `json:"rows"`
}

type ItemOutcomeRow struct {
	Kind       string `json:"kind"`
	Step       string `json:"step"`
	Transition string `json:"transition,omitempty"`
	Round      int    `json:"round"`
	Result     string `json:"result"`
	Binding    string `json:"binding,omitempty"`
	Command    string `json:"command,omitempty"`
	Model      string `json:"model"`
}

func (k *Kernel) ItemOutcome(id string) (ItemOutcome, error) {
	it, events, err := k.load(id)
	if err != nil {
		return ItemOutcome{}, err
	}
	w := k.servedWorkflow(it)
	terminal := w != nil && w.IsTerminal(it.Status)
	return itemOutcome(it, events, terminal), nil
}

type outcomeEvent struct {
	performer *PerformerRun
	gate      *gate.Result
}

func itemOutcome(it item.Item, events []ledger.Event, terminal bool) ItemOutcome {
	var ordered []outcomeEvent
	for _, e := range events {
		switch e.Kind {
		case KindPerformerRun:
			var pr PerformerRun
			if e.Decode(&pr) == nil {
				ordered = append(ordered, outcomeEvent{performer: &pr})
			}
		case gate.KindRun:
			var r gate.Result
			if e.Decode(&r) == nil && isJudgedGateRun(r) {
				ordered = append(ordered, outcomeEvent{gate: &r})
			}
		}
	}
	sortAttemptRuns(ordered, func(ev outcomeEvent) (string, int) {
		if ev.gate == nil {
			return "", 0
		}
		return ev.gate.Attempt, ev.gate.Seq
	})
	rows := make([]ItemOutcomeRow, 0, len(ordered))
	perfRounds := map[string]int{}
	gateRounds := map[[2]string]int{}
	for _, ev := range ordered {
		if ev.performer != nil {
			pr := *ev.performer
			perfRounds[pr.State]++
			res := "ok"
			if pr.Error != "" {
				res = gate.Error
			}
			rows = append(rows, ItemOutcomeRow{
				Kind:    OutcomePerformer,
				Step:    pr.State,
				Round:   perfRounds[pr.State],
				Result:  res,
				Binding: pr.Binding,
				Command: pr.Command,
				Model:   modelKey(pr.ModelID, pr.Model),
			})
			continue
		}
		r := *ev.gate
		key := [2]string{r.Transition, r.Gate}
		gateRounds[key]++
		round := gateRounds[key]
		for _, v := range verdictsOfResult(r) {
			rows = append(rows, ItemOutcomeRow{
				Kind:       OutcomeGate,
				Step:       r.Gate,
				Transition: r.Transition,
				Round:      round,
				Result:     v.result,
				Binding:    v.binding,
				Command:    v.command,
				Model:      modelKey(v.modelID, v.model),
			})
		}
	}
	return ItemOutcome{
		Item:     it.ID,
		Workflow: it.Workflow,
		Status:   it.Status,
		Terminal: terminal,
		Rows:     rows,
	}
}
