package item

import (
	"slices"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

type Seat struct {
	Item     string    `json:"item,omitempty"`
	Session  string    `json:"session"`
	Location string    `json:"location"`
	Base     string    `json:"base,omitempty"`
	BaseRef  string    `json:"base_ref,omitempty"`
	BoundAt  time.Time `json:"bound_at,omitzero"`
	Machine  string    `json:"machine,omitempty"`
	Host     string    `json:"host,omitempty"`
	User     string    `json:"user,omitempty"`
}

type SeatRelease struct {
	Session string `json:"session"`
}

type Attachment struct {
	Item      string    `json:"item,omitempty"`
	Name      string    `json:"name"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	MediaType string    `json:"media_type"`
	At        time.Time `json:"at,omitzero"`
	Actor     string    `json:"actor,omitempty"`
	Operator  bool      `json:"operator,omitempty"`
}

func HeldSeat(events []ledger.Event) bool {
	return slices.ContainsFunc(events, func(e ledger.Event) bool { return e.Kind == KindSeatBound })
}

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

func LatestAttachments(atts []Attachment) (latest []Attachment, versions map[string]int) {
	versions = map[string]int{}
	for i := len(atts) - 1; i >= 0; i-- {
		if versions[atts[i].Name] == 0 {
			latest = append(latest, atts[i])
		}
		versions[atts[i].Name]++
	}
	return latest, versions
}
