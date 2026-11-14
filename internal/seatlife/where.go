package seatlife

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
)

// Where is a seat's place in a few words, for a mark or a suffix: a local:
// seat is its machine (its host, else "machine <first 8 of its id>") and
// machine is true; a cloud: seat is "cloud"; any other location (remote:<node>)
// is the location as recorded; an empty location is "location not recorded".
func Where(seat item.Seat) (place string, machine bool) {
	loc := strings.TrimSpace(seat.Location)
	switch {
	case strings.HasPrefix(loc, item.LocationLocal):
		if seat.Host != "" {
			return seat.Host, true
		}
		if seat.Machine != "" {
			id := seat.Machine
			if len(id) > 8 {
				id = id[:8]
			}
			return "machine " + id, true
		}
		return "machine not recorded", true
	case strings.HasPrefix(loc, item.LocationCloud):
		return "cloud", false
	case loc == "":
		return "location not recorded", false
	}
	return loc, false
}
