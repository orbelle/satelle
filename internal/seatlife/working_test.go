//go:build linux || darwin

package seatlife

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

func TestWorkingDropsOnlyGone(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "live", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	if err := Own(home, "dead", killed(t)); err != nil {
		t.Fatal(err)
	}
	seats := []item.Seat{
		{Session: "live", Location: "local:/x", Machine: here},
		{Session: "dead", Location: "local:/x", Machine: here},
		{Session: "far", Location: "local:/x", Machine: "m-other", Host: "h2"},
		{Session: "none", Location: "local:/x", Machine: here},
		{Session: "dead", Location: "remote:n1", Machine: here},
	}
	var got []string
	for _, s := range Working(home, here, seats) {
		got = append(got, s.Session+"@"+s.Location)
	}
	want := []string{"live@local:/x", "far@local:/x", "none@local:/x", "dead@remote:n1"}
	if len(got) != len(want) {
		t.Fatalf("working %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("working %v, want %v", got, want)
		}
	}
	if all := Working("", here, seats); len(all) != len(seats) {
		t.Errorf("an empty home kept %d of %d seats", len(all), len(seats))
	}
	for _, st := range []Standing{Here, Elsewhere, Unverified} {
		if !st.Working() {
			t.Errorf("%s is not working", st)
		}
	}
	if Gone.Working() {
		t.Error("gone is working")
	}
}
