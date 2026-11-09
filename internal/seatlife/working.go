package seatlife

import "github.com/bobmcallan/satelle/internal/item"

// Working reports whether a seat of this standing still works its item: every
// standing but Gone. Elsewhere and unverified seats cannot be proven orphaned,
// so they keep working until released.
func (s Standing) Working() bool { return s != Gone }

// Working returns the seats still working their items, seen from this machine
// (here is its id) by the session records in home: it drops each seat whose
// StandingOf is Gone. An empty home keeps every seat (a caller with no
// session records, such as a remote node). It never writes.
func Working(home, here string, seats []item.Seat) []item.Seat {
	if home == "" {
		return seats
	}
	var out []item.Seat
	for _, s := range seats {
		if st, _ := StandingOf(home, here, s); st.Working() {
			out = append(out, s)
		}
	}
	return out
}
