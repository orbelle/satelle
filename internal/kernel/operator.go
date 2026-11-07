package kernel

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// OperatorRecordError refuses a transition out of an operator state before
// the operator has recorded an attachment of the state's declared type since
// the item entered it. Nothing is run or recorded.
type OperatorRecordError struct {
	Item, Kind, State, Type string
}

func (e *OperatorRecordError) Error() string {
	return fmt.Sprintf("%s: state %s is the operator's, and leaving it needs the operator's %s record since the item entered it. The operator records it from their own terminal, with SATELLE_SESSION unset: satelle %s record %s --file F",
		e.Item, e.State, e.Type, e.Kind, e.Item)
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
	typ, ok := w.States[it.Status].Operated()
	if !ok {
		return item.Item{}, "", fmt.Errorf("%s is in state %s, which is not the operator's: there is no operator's word to record", id, it.Status)
	}
	return it, typ, nil
}

// RecordOperator records the operator's word on id: data attached under name
// with the current state's declared type as its media-type type parameter,
// marked as the operator's. session is the agent session the call comes from
// (SATELLE_SESSION); any session is refused, so an agent cannot record it.
// The CLI verb also requires the item id retyped on the controlling terminal,
// and the hook refuses the verb in any agent tool call.
//
// The operator mark is a payload field only this method sets: Attach, the
// attach verb, performer artifacts and the server's MCP attach tool (whose
// payload is name, body and media type) never set it, and ledger append
// refuses attachment.added. If the server ever passed arbitrary payload fields
// through, this would need revisiting.
func (k *Kernel) RecordOperator(id, name string, data []byte, mediaType, actor, session string) (item.Attachment, error) {
	if err := OperatorSession(session); err != nil {
		return item.Attachment{}, err
	}
	_, typ, err := k.OperatorState(id)
	if err != nil {
		return item.Attachment{}, err
	}
	if name == "" {
		name = typ
	}
	if mediaType == "" {
		mediaType = "text/markdown"
	}
	mt := agent.TypedMediaType(mediaType, typ)
	if mt == "" {
		return item.Attachment{}, fmt.Errorf("media type %q is not valid", mediaType)
	}
	return k.attach(id, item.Attachment{Name: name, MediaType: mt, Operator: true}, data, actor)
}

// operatorRecorded reports whether events hold an operator-recorded
// attachment of type typ.
func operatorRecorded(events []ledger.Event, typ string) bool {
	for _, a := range item.FoldAttachments(events) {
		if a.Operator && agent.MediaTypeParam(a.MediaType, "type") == typ {
			return true
		}
	}
	return false
}

// OperatorSession refuses a call that comes from an agent session: the
// operator's word is never an agent's.
func OperatorSession(session string) error {
	if session != "" {
		return fmt.Errorf("an agent session (%s) may not record the operator's word; the operator records it from their own terminal with SATELLE_SESSION unset", session)
	}
	return nil
}
