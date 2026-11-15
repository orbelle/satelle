package seatlife

import "github.com/bobmcallan/satelle/internal/item"

func (s Standing) Working() bool { return s != Gone }

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
