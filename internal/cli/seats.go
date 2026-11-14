package cli

import (
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// editScope is what a seat on the item lets its session change: the item's
// current state's workflow.State.EditScope, empty for an unknown workflow or
// a state no agent performs.
func editScope(k *kernel.Kernel, it item.Item) string {
	w, ok := k.Workflows[it.Workflow]
	if !ok {
		return ""
	}
	return w.States[it.Status].EditScope()
}

// seatsOf lists session's bound seats with their items' positions.
func seatsOf(k *kernel.Kernel, session string) ([]seatView, error) {
	seats, err := k.Cache.Seats(session, "")
	if err != nil {
		return nil, err
	}
	var out []seatView
	for _, s := range seats {
		it, ok, err := k.Cache.Item(s.Item)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		edits := editScope(k, it)
		out = append(out, seatView{Item: it.ID, Title: it.Title, Status: it.Status, Location: s.Location, Performing: edits != "", Edits: edits})
	}
	return out, nil
}

func printSeats(w io.Writer, seats []seatView) {
	if len(seats) == 0 {
		fmt.Fprintln(w, "seats     none")
		return
	}
	fmt.Fprintln(w, "seats")
	for _, s := range seats {
		var mark string
		switch s.Edits {
		case workflow.EditsTree:
			mark = fmt.Sprintf(" (performing %s: edits allowed at this location)", s.Status)
		case workflow.EditsAttachments:
			mark = fmt.Sprintf(" (performing %s: attachments only)", s.Status)
		default:
			mark = fmt.Sprintf(" (%s: no edits)", s.Status)
		}
		fmt.Fprintf(w, "  %s  %s  [%s] at %s%s\n", s.Item, s.Title, s.Status, s.Location, mark)
	}
}
