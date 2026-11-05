package item

import (
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// Seat binds a session at a location to an item. Base is the commit the seat
// started from.
type Seat struct {
	Item     string    `json:"item,omitempty"`
	Session  string    `json:"session"`
	Location string    `json:"location"`
	Base     string    `json:"base,omitempty"`
	BoundAt  time.Time `json:"bound_at,omitzero"`
}

// SeatRelease is the payload of seat.released.
type SeatRelease struct {
	Session string `json:"session"`
}

// Attachment is a document recorded on an item; its body is a blob.
type Attachment struct {
	Item      string    `json:"item,omitempty"`
	Name      string    `json:"name"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	MediaType string    `json:"media_type"`
	At        time.Time `json:"at,omitzero"`
	Actor     string    `json:"actor,omitempty"`
}

// FoldSeats returns the seats still bound at the end of the chain, in bind
// order. Binding the same session again replaces its seat.
func FoldSeats(events []ledger.Event) []Seat {
	var seats []Seat
	drop := func(session string) {
		out := seats[:0]
		for _, s := range seats {
			if s.Session != session {
				out = append(out, s)
			}
		}
		seats = out
	}
	for _, e := range events {
		switch e.Kind {
		case KindSeatBound:
			var s Seat
			if e.Decode(&s) != nil {
				continue
			}
			s.Item, s.BoundAt = e.Item, e.At
			drop(s.Session)
			seats = append(seats, s)
		case KindSeatReleased:
			var r SeatRelease
			if e.Decode(&r) == nil {
				drop(r.Session)
			}
		}
	}
	return seats
}

// FoldAttachments returns every attachment on the chain in order.
func FoldAttachments(events []ledger.Event) []Attachment {
	var out []Attachment
	for _, e := range events {
		if e.Kind != KindAttachmentAdd {
			continue
		}
		var a Attachment
		if e.Decode(&a) != nil {
			continue
		}
		a.Item, a.At, a.Actor = e.Item, e.At, e.Actor
		out = append(out, a)
	}
	return out
}
