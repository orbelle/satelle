package item

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func seatEvent(t *testing.T, s Seat) ledger.Event {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Event{Kind: KindSeatBound, Payload: b}
}

func TestSeatedCheckout(t *testing.T) {
	a := seatEvent(t, Seat{Session: "s1", Checkout: "/a"})
	b := seatEvent(t, Seat{Session: "s2", Checkout: "/b"})
	none := seatEvent(t, Seat{Session: "s3"})
	cases := []struct {
		events  []ledger.Event
		want    string
		seated  bool
		comment string
	}{
		{nil, "", false, "no seat"},
		{[]ledger.Event{none}, "", true, "a seat without a checkout"},
		{[]ledger.Event{a, b}, "/b", true, "the last recorded checkout"},
		{[]ledger.Event{a, none}, "/a", true, "a seat without a checkout keeps the recorded one"},
	}
	for _, c := range cases {
		if got, seated := SeatedCheckout(c.events); got != c.want || seated != c.seated {
			t.Errorf("%s: SeatedCheckout = %q, %v; want %q, %v", c.comment, got, seated, c.want, c.seated)
		}
	}
}
