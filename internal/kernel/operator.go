package kernel

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

type OperatorRecordError struct {
	Item, Kind, State, Type string
}

func (e *OperatorRecordError) Error() string {
	return fmt.Sprintf("%s: state %s is the operator's, and leaving it needs the operator's %s record since the item entered it. The operator records it from their own terminal, with no session named (SATELLE_SESSION unset, no --session): satelle %s record %s --file F",
		e.Item, e.State, e.Type, e.Kind, e.Item)
}

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

func operatorRecorded(events []ledger.Event, typ string) bool {
	for _, a := range item.FoldAttachments(events) {
		if a.Operator && agent.MediaTypeParam(a.MediaType, "type") == typ {
			return true
		}
	}
	return false
}

func OperatorSession(session string) error {
	if session != "" {
		return fmt.Errorf("an agent session (%s) may not record the operator's word; the operator records it from their own terminal with no session named (SATELLE_SESSION unset, no --session)", session)
	}
	return nil
}
