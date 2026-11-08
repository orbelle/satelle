package kernel

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/operator"
)

// OperatorRecordError refuses a transition out of an operator state before
// the operator has recorded an attachment of the state's declared type, naming
// a move, since the item entered it. Nothing is run or recorded.
type OperatorRecordError struct {
	Item, Kind, State, Type string
}

func (e *OperatorRecordError) Error() string {
	return fmt.Sprintf("%s: state %s is the operator's, and leaving it needs the operator's %s record, naming the move, since the item entered it. The operator records it from their own terminal, with SATELLE_SESSION unset: satelle %s record %s --to <state> --file F",
		e.Item, e.State, e.Type, e.Kind, e.Item)
}

// OperatorMoveError refuses a transition out of an operator state to a state
// other than the one the operator's newest record since entry names. Nothing
// is run or recorded.
type OperatorMoveError struct {
	Item, Kind, State, Type, Recorded, Asked string
}

func (e *OperatorMoveError) Error() string {
	return fmt.Sprintf("the operator's %s on %s names the move to %s; the move to %s is refused (present it with satelle %s set %s --status %s)",
		e.Type, e.Item, e.Recorded, e.Asked, e.Kind, e.Item, e.Recorded)
}

// OperatorState is the item and the attachment type its current state needs
// the operator to record; an error when that state is not the operator's.
func (k *Kernel) OperatorState(id string) (item.Item, string, error) {
	it, _, err := k.load(id)
	if err != nil {
		return item.Item{}, "", err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return item.Item{}, "", err
	}
	typ, err := operator.State(w, it)
	if err != nil {
		return item.Item{}, "", err
	}
	return it, typ, nil
}

// OperatorWord is the operator's newest record on id since the item entered
// its current state, and that state's declared type. typ is empty when the
// state is not the operator's; ok is false when nothing is recorded yet.
func (k *Kernel) OperatorWord(id string) (rec item.Attachment, typ string, ok bool, err error) {
	it, events, err := k.load(id)
	if err != nil {
		return item.Attachment{}, "", false, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return item.Attachment{}, "", false, err
	}
	typ, operated := w.States[it.Status].Operated()
	if !operated {
		return item.Attachment{}, "", false, nil
	}
	rec, ok = operator.Latest(events, typ)
	return rec, typ, ok, nil
}

// RecordOperator records the operator's word on id, naming the move to `to`,
// through the one owner of the rule (internal/operator). session is the agent
// session the call comes from (SATELLE_SESSION); any session is refused, so an
// agent cannot record it. The CLI verb also requires the item id retyped on
// the controlling terminal, and the hook refuses the verb in any agent tool
// call.
//
// The operator mark is a payload field only internal/operator sets: Attach,
// the attach verb, performer artifacts and the server's MCP attach tool (whose
// payload is name, body and media type) never set it, and ledger append
// refuses attachment.added. If the server ever passed arbitrary payload fields
// through, this would need revisiting.
func (k *Kernel) RecordOperator(id, name, to string, data []byte, mediaType, actor, session string) (item.Attachment, error) {
	if err := operator.Session(session); err != nil {
		return item.Attachment{}, err
	}
	a, _, err := operator.Record(k.Ledger, k.Workflows, id, name, to, data, mediaType, actor)
	if err != nil {
		return item.Attachment{}, err
	}
	return a, k.Cache.SyncItem(k.Ledger, id)
}

// Targets lists the states id can move to in one move from its current state.
func (k *Kernel) Targets(id string) ([]string, error) {
	it, events, err := k.load(id)
	if err != nil {
		return nil, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return nil, err
	}
	return w.Targets(it.Status, item.PriorStatus(events)), nil
}
