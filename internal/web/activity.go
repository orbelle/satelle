package web

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

type activityRowVM struct {
	rowVM
	Working         []string
	Step, StepTitle string
	StepAt          time.Time
}

func activeItem(class string, seated bool) bool {
	switch class {
	case "terminal":
		return false
	case "active", "park":
		return true
	}
	return seated
}

func activityRows(stories, executions []Item, workflows map[string]*mirror.Workflow, seats []mirror.Seat, events []Entry, standing seatStanding) []activityRowVM {
	bySeat := seatsByItem(seats)
	var active []Item
	for _, it := range append(append([]Item(nil), stories...), executions...) {
		if activeItem(stateClass(itemWorkflow(workflows, it), it.Status), len(bySeat[it.ID]) > 0) {
			active = append(active, it)
		}
	}
	ids := map[string]bool{}
	for _, it := range active {
		ids[it.ID] = true
	}
	byItem := eventsByItem(events, ids)
	out := make([]activityRowVM, 0, len(active))
	for _, it := range active {
		w := itemWorkflow(workflows, it)
		row := activityRowVM{rowVM: rowVM{Item: it, StateClass: stateClass(w, it.Status)}, StepAt: it.UpdatedAt}
		row.Assignees = assigneeVMs(assigneesOf(it, bySeat[it.ID], standing))
		held := bySeat[it.ID]
		sort.SliceStable(held, func(i, j int) bool { return held[i].BoundAt.Before(held[j].BoundAt) })
		for _, s := range held {
			st, _ := standing(s)
			row.Working = append(row.Working, s.Session+" · "+s.Location+standingSuffix(s, st))
		}
		if len(row.Working) == 0 {
			if st, ok := workflowState(w, it.Status); ok && st.PerformerAgent != "" {
				row.Working = []string{st.PerformerAgent}
			}
		}
		if step, title, at, ok := latestStep(byItem[it.ID]); ok {
			row.Step, row.StepTitle, row.StepAt = step, title, at
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StepAt.Equal(out[j].StepAt) {
			return out[i].StepAt.After(out[j].StepAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func eventsByItem(events []Entry, ids map[string]bool) map[string][]Entry {
	byItem := map[string][]Entry{}
	for _, e := range events {
		if ids[e.Item] {
			byItem[e.Item] = append(byItem[e.Item], e)
		}
	}
	return byItem
}

func standingSuffix(s mirror.Seat, st seatlife.Standing) string {
	switch st {
	case seatlife.Here:
		return ""
	case seatlife.Elsewhere:
		place, _ := seatlife.Where(seatOf(s))
		return " · elsewhere (" + place + ")"
	case seatlife.Gone:
		return " · session gone"
	}
	return " · unverified"
}

func latestStep(events []Entry) (step, title string, at time.Time, ok bool) {
	es := append([]Entry(nil), events...)
	sortEntries(es)
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		switch e.Kind {
		case mirror.EventGateRun:
			var g mirror.GateRun
			if json.Unmarshal(e.Payload, &g) != nil {
				continue
			}
			return g.Gate + " " + g.Result, g.Transition, e.At, true
		case mirror.EventTransitioned:
			if from, to, ok := transitionOf(e); ok {
				return from + " → " + to, "", e.At, true
			}
		}
	}
	return "", "", time.Time{}, false
}
