package cli

import (
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// performing reports whether an agent performs the item's current state: an
// operator state has no agent performer, so its seat allows no edits.
func performing(k *kernel.Kernel, it item.Item) bool {
	w, ok := k.Workflows[it.Workflow]
	if !ok {
		return false
	}
	return w.States[it.Status].Agent() != nil
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
		out = append(out, seatView{Item: it.ID, Title: it.Title, Status: it.Status, Location: s.Location, Performing: performing(k, it)})
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
		mark := ""
		if s.Performing {
			mark = " (performing: edits allowed at this location)"
		}
		fmt.Fprintf(w, "  %s  %s  [%s] at %s%s\n", s.Item, s.Title, s.Status, s.Location, mark)
	}
}
