package web

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// activityRowVM is one Activity row: an item being worked now, who or what
// works it, and its latest step.
type activityRowVM struct {
	rowVM
	// Working lists each seat as "session · location", with a suffix for a
	// seat not here (· elsewhere (<host>), · session gone, · unverified), else
	// the current state's performer agent; empty shows —.
	Working []string
	// Step is the latest gate result or transition; StepTitle qualifies it.
	Step, StepTitle string
	// StepAt is the latest step's time, else the item's last update: the
	// row's order and its time cell.
	StepAt time.Time
}

// activeItem decides from the pushed workflow alone whether an item is being
// worked: a terminal state never is, an active or parked state always is, and
// a start state (or one the workflow does not name) only while a seat holds it.
func activeItem(class string, seated bool) bool {
	switch class {
	case "terminal":
		return false
	case "active", "park":
		return true
	}
	return seated
}

// activityRows is the Activity panel: the stories and task runs being worked
// now, the most recent step first.
func activityRows(stories, executions []Item, workflows map[string]*mirror.Workflow, seats []mirror.Seat, events []Entry, standing seatStanding) []activityRowVM {
	bySeat := map[string][]mirror.Seat{}
	for _, s := range seats {
		if id := seatItem(s); id != "" {
			bySeat[id] = append(bySeat[id], s)
		}
	}
	var active []Item
	for _, it := range append(append([]Item(nil), stories...), executions...) {
		if activeItem(stateClass(workflows[it.Workflow], it.Status), len(bySeat[it.ID]) > 0) {
			active = append(active, it)
		}
	}
	byItem := map[string][]Entry{}
	ids := map[string]bool{}
	for _, it := range active {
		ids[it.ID] = true
	}
	for _, e := range events {
		if ids[e.Item] {
			byItem[e.Item] = append(byItem[e.Item], e)
		}
	}
	out := make([]activityRowVM, 0, len(active))
	for _, it := range active {
		w := workflows[it.Workflow]
		row := activityRowVM{rowVM: rowVM{Item: it, StateClass: stateClass(w, it.Status)}, StepAt: it.UpdatedAt}
		held := bySeat[it.ID]
		sort.SliceStable(held, func(i, j int) bool { return held[i].BoundAt.Before(held[j].BoundAt) })
		for _, s := range held {
			row.Working = append(row.Working, s.Session+" · "+s.Location+standingSuffix(s, standing(s)))
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

// standingSuffix marks an Activity seat that is not here.
func standingSuffix(s mirror.Seat, st seatlife.Standing) string {
	switch st {
	case seatlife.Here:
		return ""
	case seatlife.Elsewhere:
		if s.Host != "" {
			return " · elsewhere (" + s.Host + ")"
		}
		return " · elsewhere"
	case seatlife.Gone:
		return " · session gone"
	}
	return " · unverified"
}

// latestStep is the newest gate result ("<gate> <result>", titled with its
// transition) or transition ("<from> → <to>") among one item's events.
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
