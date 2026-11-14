package kernel

import (
	"slices"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The acts a Move names.
const (
	// MoveFire fires the queue Target names (satelle queue fire).
	MoveFire = "fire"
	// MoveSet moves Target to the state To (satelle <kind> set).
	MoveSet = "set"
	// MoveSeat seats the session on Target, a run no session holds.
	MoveSeat = "seat"
)

// Move is the next move a seated item owes its session. Item and Status are
// the seated item's. Act is MoveFire (Target is the queue's file name),
// MoveSet (Target is the item to move, To its next state) or MoveSeat (Target
// is a run no session holds); TargetKind and TargetStatus are Target's kind
// and state when it is an item. For a move on a run, Queue is the queue whose
// last run it is and Carries says whether the run took Item. An empty Act
// means the item owes no move.
type Move struct {
	Item, Status            string
	Act, Target, TargetKind string
	TargetStatus, To, Queue string
	Carries                 bool
}

// Owed reports whether the move names something to do.
func (m Move) Owed() bool { return m.Act != "" }

// waitsHere reports whether a turn may end with an item at st without a
// move: a terminal or park state, a state that declares what it waits on, or
// the operator's state. Only the workflow file says so.
func waitsHere(st workflow.State) bool {
	if st.Terminal || st.Park || st.Waits != "" {
		return true
	}
	_, operated := st.Operated()
	return operated
}

// NextMove is the move item id owes session, the session seated on it. It
// only reads. A state the workflow declares a stop at (terminal, park,
// waits, the operator's) owes nothing. A state that enqueues is routed by its
// queues, in the state's order: the open last run of a queue the item is in
// (or that run carries) is the move (driving it when this session holds it,
// seating it when no session does, nothing when another session does), and a
// queue Fire would accept is fired. A queue that is the item's route but
// cannot fire (a terminal queue, no trigger, a bad base, an unreadable queue)
// owes nothing. Otherwise the move is the item's next transition.
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
				routed = true // an unreadable queue is the route; it names nothing
				continue
			}
			run, open := k.lastOpenRun(qs)
			in := slices.Contains(m.Members, it.ID)
			carries := open && slices.Contains(k.runInputs(run.ID), it.ID)
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
			if _, err := k.fireReady(q.ID, ""); err == nil {
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

// runMove is the move an open run owes session: none when the run's state is
// a stop its workflow declares or another session holds it, its next
// transition when session holds it, a seat when no session does.
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
