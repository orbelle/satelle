package kernel

import (
	"slices"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	MoveFire = "fire"
	MoveSet  = "set"
	MoveSeat = "seat"
)

type Move struct {
	Item, Status            string
	Act, Target, TargetKind string
	TargetStatus, To, Queue string
	Carries                 bool
}

func (m Move) Owed() bool { return m.Act != "" }

func waitsHere(st workflow.State) bool {
	if st.Terminal || st.Park || st.Waits != "" {
		return true
	}
	_, operated := st.Operated()
	return operated
}

func (k *Kernel) NextMove(id, session string) (Move, error) {
	it, events, err := k.load(id)
	if err != nil {
		return Move{}, err
	}
	none := Move{Item: it.ID, Status: it.Status}
	w := k.Workflows[it.Workflow]
	if w == nil {
		return none, nil
	}
	st, ok := w.States[it.Status]
	if !ok || waitsHere(st) {
		return none, nil
	}
	if len(st.Enqueue) > 0 {
		var runMove *Move
		routed := false
		for _, name := range st.Enqueue {
			q, qs, m, err := k.QueueMembers(name)
			if err != nil {
				routed = true
				continue
			}
			in := slices.Contains(m.Members, it.ID)
			var run item.Item
			open, carries := false, false
			if q.Trigger != nil && q.Trigger.Overlaps() {
				held, unsettled, err := k.holdings(qs)
				for _, h := range held {
					if slices.Contains(h.inputs, it.ID) {
						run, open, carries = h.run, true, true
						break
					}
				}
				if err != nil || unsettled != nil {
					routed = routed || in || carries
					continue
				}
			} else {
				run, open = k.lastOpenRun(qs)
				if open {
					_, rec, _, _ := k.taskRun(run.ID)
					carries = slices.Contains(rec.Inputs, it.ID)
				}
			}
			if !in && !carries {
				continue
			}
			routed = true
			if open {
				if runMove == nil {
					if mv, ok := k.runMove(none, run, session); ok {
						mv.Queue, mv.Carries = name, carries
						runMove = &mv
					}
				}
				continue
			}
			if r, err := k.fireReady(q.ID, ""); err == nil && slices.Contains(r.members, it.ID) {
				mv := none
				mv.Act, mv.Target = MoveFire, name
				return mv, nil
			}
		}
		if runMove != nil {
			return *runMove, nil
		}
		if routed {
			return none, nil
		}
	}
	next := nextTarget(w, it.Status, item.PriorStatus(events))
	if next == "" {
		return none, nil
	}
	mv := none
	mv.Act, mv.Target, mv.TargetKind, mv.TargetStatus, mv.To = MoveSet, it.ID, it.Kind, it.Status, next
	return mv, nil
}

func (k *Kernel) runMove(base Move, run item.Item, session string) (Move, bool) {
	w := k.Workflows[run.Workflow]
	if w == nil {
		return base, false
	}
	st, ok := w.States[run.Status]
	if !ok || waitsHere(st) {
		return base, false
	}
	seats, err := k.Cache.Seats("", run.ID)
	if err != nil {
		return base, false
	}
	mv := base
	mv.Target, mv.TargetKind, mv.TargetStatus = run.ID, run.Kind, run.Status
	if len(seats) == 0 {
		mv.Act = MoveSeat
		return mv, true
	}
	mine := false
	for _, s := range seats {
		if s.Session != session {
			return base, false
		}
		mine = true
	}
	if !mine {
		return base, false
	}
	_, events, err := k.load(run.ID)
	if err != nil {
		return base, false
	}
	next := nextTarget(w, run.Status, item.PriorStatus(events))
	if next == "" {
		return base, false
	}
	mv.Act, mv.To = MoveSet, next
	return mv, true
}
