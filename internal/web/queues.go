package web

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

type queueVM struct {
	rowVM
	Filter   string
	Trigger  string
	Members  []memberVM
	LastFire *lastFireVM
}

type memberVM struct {
	ID, Kind, Title, Status string
	Explicit                bool
}

type lastFireVM struct {
	item.Fired
	RunStatus string
	Inputs    int
}

func asItem(it Item) item.Item {
	return item.Item{ID: it.ID, Kind: it.Kind, Title: it.Title, Status: it.Status, Tags: it.Tags,
		Parent: it.ParentID, Category: it.Category, Workflow: it.Workflow, Inputs: it.Inputs, Trigger: it.Trigger, Repo: it.Repo}
}

func filterText(s *item.Selection) string {
	if s == nil {
		return "added items only"
	}
	out := ""
	for _, kv := range [][2]string{{"kind", s.Kind}, {"status", s.Status}, {"tag", s.Tag}, {"category", s.Category}} {
		if kv[1] != "" {
			if out != "" {
				out += " "
			}
			out += kv[0] + ":" + kv[1]
		}
	}
	if s.Repo != "" {
		if out != "" {
			out += " "
		}
		out += "repo:" + reporeg.Literal(s.Repo)
	}
	if out == "" {
		return "every item"
	}
	return out
}

func triggerLabel(t *item.Trigger) string {
	if t == nil {
		return "none"
	}
	target := "task " + t.Task
	if t.Workflow != "" {
		target = "workflow " + t.Workflow
	}
	if t.MinSize == 0 {
		return target + ", on queue fire"
	}
	return fmt.Sprintf("%s, at %d", target, t.MinSize)
}

func queueRows(queues, all []Item, events []Entry, workflows map[string]*mirror.Workflow) []queueVM {
	byID := map[string]Item{}
	cands := make([]item.Item, 0, len(all))
	for _, it := range all {
		byID[it.ID] = it
		cands = append(cands, asItem(it))
	}
	chains := map[string][]ledger.Event{}
	for _, e := range events {
		chains[e.Item] = append(chains[e.Item], ledger.Event{ID: e.ID, Item: e.Item, Kind: e.Kind, At: e.At, Actor: e.Actor, Payload: e.Payload})
	}
	status := func(id string) (string, bool, bool) {
		it, ok := byID[id]
		if !ok {
			return "", false, false
		}
		s, _ := workflowState(workflows[it.Workflow], it.Status)
		return it.Status, s.Terminal, true
	}
	target := func(id string) (string, bool) {
		it, ok := byID[id]
		if !ok {
			return "", false
		}
		return asItem(it).Target(), true
	}
	out := make([]queueVM, 0, len(queues))
	for _, q := range queues {
		st := item.FoldQueue(chains[q.ID])
		vm := queueVM{
			rowVM:   rowVM{Item: q, StateClass: stateClass(workflows[q.Workflow], q.Status)},
			Filter:  filterText(q.Inputs),
			Trigger: triggerLabel(q.Trigger),
		}
		for _, id := range item.Members(asItem(q), st, cands, status, target).Members {
			m := memberVM{ID: id}
			if it, ok := byID[id]; ok {
				m.Kind, m.Title, m.Status = it.Kind, it.Title, it.Status
			}
			for _, e := range st.Explicit {
				m.Explicit = m.Explicit || e == id
			}
			vm.Members = append(vm.Members, m)
		}
		if lf := st.LastFire; lf != nil {
			lv := &lastFireVM{Fired: *lf, RunStatus: "missing"}
			if run, ok := byID[lf.Run]; ok {
				lv.RunStatus = run.Status
			}
			for _, e := range chains[lf.Run] {
				var rec struct{ Inputs []string }
				if e.Kind == "task.run" && e.Decode(&rec) == nil {
					lv.Inputs = len(rec.Inputs)
				}
			}
			vm.LastFire = lv
		}
		out = append(out, vm)
	}
	return out
}
