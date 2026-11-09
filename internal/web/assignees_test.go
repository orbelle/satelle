//go:build linux || darwin

package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// assigneesOf keeps the explicit users, adds each user of a working seat
// marked working, and drops a seat whose standing is gone (sto_600b9a85 AC3).
func TestAssigneesOfDropsGoneSeats(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	it := Item{ID: "sto_1", Assignees: []string{"ann"}}
	seats := []mirror.Seat{
		{Item: "sto_1", Session: "s-cy", User: "cy", BoundAt: t0},
		{Item: "sto_1", Session: "s-bob", User: "bob", BoundAt: t0.Add(time.Minute)},
		{Item: "sto_1", Session: "s-far", User: "dee", BoundAt: t0.Add(2 * time.Minute)},
	}
	standing := func(s mirror.Seat) seatlife.Standing {
		switch s.Session {
		case "s-cy":
			return seatlife.Gone
		case "s-far":
			return seatlife.Elsewhere
		}
		return seatlife.Here
	}
	rows := []rowVM{{Item: it}}
	withAssignees(rows, seatsByItem(seats), func(s mirror.Seat) (seatlife.Standing, seatlife.Evidence) { return standing(s), "" })
	want := []assigneeVM{{User: "ann"}, {User: "bob", Working: true}, {User: "dee", Working: true}}
	got := rows[0].Assignees
	if len(got) != len(want) {
		t.Fatalf("assignees %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("assignees %+v, want %+v", got, want)
		}
	}
}

// satelled's story row and detail list the explicit assignee and each user
// of a live seat, marking the seated one working; a seat whose session is
// gone adds no one (sto_600b9a85 AC3).
func TestSatelledShowsAssignees(t *testing.T) {
	seats, home := fourSeatHome(t, "sto_1", "sto_far", "sto_1", "sto_old")
	bob, cy := seats[0], seats[2] // s-here (alive) and s-gone, both on sto_1
	bob.User, cy.User = "bob", "cy"
	snap := fixtureSnapshot()
	snap.Seats = []json.RawMessage{raw(bob), raw(cy)}
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)

	body := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	start := strings.Index(body, rowFor(body, "sto_1"))
	if start < 0 {
		t.Fatalf("no row for sto_1:\n%s", body)
	}
	row := body[start : start+strings.Index(body[start:], "</tr>")]
	for _, want := range []string{`data-assignee="ann,bob"`, `ann, bob (working)`} {
		if !strings.Contains(row, want) {
			t.Errorf("story row lacks %q: %s", want, row)
		}
	}
	if strings.Contains(row, "ann (working)") || strings.Contains(row, "cy") {
		t.Errorf("story row marks ann working or lists cy: %s", row)
	}

	detail := httpGetBody(t, srv.URL+"/r/proj/story/sto_1")
	if !strings.Contains(detail, "<dt>Assignees</dt><dd>ann, bob (working)</dd>") {
		t.Errorf("detail lacks the assignees:\n%s", detail)
	}
	if strings.Contains(detail, ">cy") || strings.Contains(detail, "cy (working)") {
		t.Errorf("detail lists cy, whose session is gone")
	}
}
