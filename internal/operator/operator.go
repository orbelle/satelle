// Package operator owns the operator's word on an item in an operator state:
// who may record it, what it must name, and which record holds. The kernel
// (the CLI's record verb and every transition) and satelled's Awaiting-you
// page both record and read it through here, so the rule has one owner. It
// links no kernel, CLI or process stack, and names no state: every state and
// type comes from the workflow.
package operator

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// Session refuses a call that comes from an agent session: the operator's
// word is never an agent's.
func Session(session string) error {
	if session != "" {
		return fmt.Errorf("an agent session (%s) may not record the operator's word; the operator records it from their own terminal with SATELLE_SESSION unset", session)
	}
	return nil
}

// State is the attachment type the item's current state needs the operator to
// record; an error when that state is not the operator's.
func State(w *workflow.Workflow, it item.Item) (string, error) {
	typ, ok := w.States[it.Status].Operated()
	if !ok {
		return "", fmt.Errorf("%s is in state %s, which is not the operator's: there is no operator's word to record", it.ID, it.Status)
	}
	return typ, nil
}

// Latest is the newest operator-marked attachment of type typ that names a
// move, among events since the item entered its current state; ok is false
// when there is none. An earlier visit's record never counts.
func Latest(events []ledger.Event, typ string) (item.Attachment, bool) {
	atts := item.FoldAttachments(item.SinceEntered(events))
	for i := len(atts) - 1; i >= 0; i-- {
		a := atts[i]
		if a.Operator && a.To != "" && item.MediaTypeParam(a.MediaType, "type") == typ {
			return a, true
		}
	}
	return item.Attachment{}, false
}

// Record records the operator's word on id: data attached under name (default
// the state's declared type) with that type as its media-type type parameter,
// marked as the operator's and naming the move to `to`. The item must be in an
// operator state, `to` a move declared from it, and the word not empty. The
// caller refuses an agent session (Session) and syncs any cache.
func Record(l *ledger.Store, workflows map[string]*workflow.Workflow, id, name, to string, data []byte, mediaType, actor string) (item.Attachment, ledger.Event, error) {
	if !ledger.ValidItem(id) {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("invalid item id %q", id)
	}
	events, err := l.Read(id)
	if err != nil {
		return item.Attachment{}, ledger.Event{}, err
	}
	if len(events) == 0 {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("item %s does not exist", id)
	}
	it, err := item.Fold(events)
	if err != nil {
		return item.Attachment{}, ledger.Event{}, err
	}
	w, ok := workflows[it.Workflow]
	if !ok {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("item %s names workflow %q, which does not exist", id, it.Workflow)
	}
	typ, err := State(w, it)
	if err != nil {
		return item.Attachment{}, ledger.Event{}, err
	}
	if strings.TrimSpace(to) == "" {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("the operator's word on %s names no move: name the state it moves to (one of %s)", id, strings.Join(w.Targets(it.Status, item.PriorStatus(events)), ", "))
	}
	if _, err := w.Find(it.Status, to, item.PriorStatus(events)); err != nil {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("the operator's word on %s names the move to %s, which state %s does not declare (one of %s)", id, to, it.Status, strings.Join(w.Targets(it.Status, item.PriorStatus(events)), ", "))
	}
	if strings.TrimSpace(string(data)) == "" {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("the operator's word on %s is empty: say why (a send-back records its reason)", id)
	}
	if name == "" {
		name = typ
	}
	if mediaType == "" {
		mediaType = "text/markdown"
	}
	mt := item.TypedMediaType(mediaType, typ)
	if mt == "" {
		return item.Attachment{}, ledger.Event{}, fmt.Errorf("media type %q is not valid", mediaType)
	}
	return item.AppendAttachment(l, id, item.Attachment{Name: name, MediaType: mt, Operator: true, To: to}, data, actor)
}
