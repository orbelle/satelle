package seatlife

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// Where names a seat's place: another machine by its host, else its id's
// first eight characters; a remote by its location; a cloud seat as cloud;
// a seat with no location as not recorded.
func TestWhere(t *testing.T) {
	for _, c := range []struct {
		name    string
		seat    item.Seat
		place   string
		machine bool
	}{
		{"machine with host", item.Seat{Location: "local:/repos/x", Machine: "m-other-0123456789", Host: "other-host"}, "other-host", true},
		{"machine without host", item.Seat{Location: "local:/repos/x", Machine: "m-other-0123456789"}, "machine m-other-", true},
		{"machine not recorded", item.Seat{Location: "local:/repos/x"}, "machine not recorded", true},
		{"remote", item.Seat{Location: "remote:nod_7"}, "remote:nod_7", false},
		{"cloud", item.Seat{Location: "cloud:https://claude.ai/code/session_x"}, "cloud", false},
		{"no location", item.Seat{}, "location not recorded", false},
	} {
		place, machine := Where(c.seat)
		if place != c.place || machine != c.machine {
			t.Errorf("%s: Where = %q, %v; want %q, %v", c.name, place, machine, c.place, c.machine)
		}
	}
}
