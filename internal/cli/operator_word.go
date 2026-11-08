package cli

import (
	"time"

	"github.com/bobmcallan/satelle/internal/kernel"
)

// operatorWordView is story get's operator line: the type the item's operator
// state needs, and the move the operator chose (empty To while awaiting it).
type operatorWordView struct {
	Type  string    `json:"type"`
	To    string    `json:"to,omitempty"`
	Actor string    `json:"actor,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

// operatorWordOf is the operator's word on id while it is in an operator
// state; nil when its state is not the operator's.
func operatorWordOf(k *kernel.Kernel, id string) (*operatorWordView, error) {
	rec, typ, ok, err := k.OperatorWord(id)
	if err != nil || typ == "" {
		return nil, err
	}
	v := &operatorWordView{Type: typ}
	if ok {
		v.To, v.Actor, v.At = rec.To, rec.Actor, rec.At
	}
	return v, nil
}
