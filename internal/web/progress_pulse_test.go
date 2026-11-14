package web

import (
	"encoding/json"
	"html"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// sto_96f0ad26 AC3, AC4: on the Stories list a story's Progress chip pulses
// exactly when the story is engaged, the same set the row's data-engaged and
// the header's engaged and elsewhere badges count; a story at a step that
// nobody works shows a still chip that says so, and a pulsing chip's hover
// says where and by whom.
func TestProgressPulsesOnlyWorkedStories(t *testing.T) {
	w := progressWorkflow(same)
	var items []Item
	var events []Entry
	story := func(id string, h *hist, status string) *hist {
		items = append(items, Item{ID: id, Kind: "story", Title: id, Status: status, Workflow: w.Name})
		events = append(events, h.evs...)
		return h
	}
	at2 := func(id string) *hist {
		return story(id, newHist(id, same).place("s0").move("s0", "s1").move("s1", "s2"), "s2")
	}
	seated, disp, far, node := at2("sto_seated"), at2("sto_disp"), at2("sto_far"), at2("sto_node")
	at2("sto_parked")
	placed := story("sto_placed", newHist("sto_placed", same).place("s0"), "s0")

	seat := func(h *hist, session, user string) mirror.Seat {
		return mirror.Seat{ID: h.id + "/" + session, Item: h.id, Session: session, User: user, Location: "local:/p", BoundAt: h.last()}
	}
	// Here: owned by this live test process on this machine. seatsHere sets no
	// location, and Classify reads a non-local: location as elsewhere, so
	// each carries local:/p.
	here, home := seatsHere(t, []json.RawMessage{
		raw(seat(seated, "s-user", "bob")),
		raw(seat(disp, "perform_ab12", "bob")),
		raw(seat(placed, "s-placed", "bob")),
	})
	farSeat := seat(far, "s-far", "ann")
	farSeat.Machine, farSeat.Host = "m-other", "other-host"
	nodeSeat := seat(node, "s-node", "bob")
	nodeSeat.Location = "remote:nod_7"

	snap := progressSnapshot([]*mirror.Workflow{w}, items, nil, events, nil)
	snap.Seats = append(here, raw(farSeat), raw(nodeSeat))
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)

	// Guard the fixture: three seats here, two elsewhere.
	standing := standingHere()
	for _, r := range snap.Seats {
		var s mirror.Seat
		if err := json.Unmarshal(r, &s); err != nil {
			t.Fatal(err)
		}
		want := seatlife.Here
		if s.Session == "s-far" || s.Session == "s-node" {
			want = seatlife.Elsewhere
		}
		if got, ev := standing(s); got != want {
			t.Fatalf("fixture: seat %s stands %v (%s), want %v", s.ID, got, ev, want)
		}
	}

	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	ids := []string{"sto_seated", "sto_disp", "sto_far", "sto_node", "sto_parked", "sto_placed"}
	row := func(id string) string {
		r := fullRowFor(frag, id)
		if r == "" {
			t.Fatalf("no row for %s:\n%s", id, frag)
		}
		return r
	}
	// A row pulses when one of its chips carries a class the served
	// stylesheet animates: what the operator sees, not only the class name.
	animated := animatedClasses(t, httpGetBody(t, srv.URL+"/static/app.css"))
	chipClass := regexp.MustCompile(`class="stage-chip ([^"]*)"`)
	var pulsing, chipped []string
	for _, id := range ids {
		for _, m := range chipClass.FindAllStringSubmatch(row(id), -1) {
			if animated[m[1]] {
				pulsing = append(pulsing, id)
				break
			}
		}
		if strings.Contains(row(id), "stage-chip") {
			chipped = append(chipped, id)
		}
	}
	engaged := engagedRows(frag)

	// AC4: the pulsing rows are the engaged rows that have a chip.
	if want := []string{"sto_disp", "sto_far", "sto_node", "sto_seated"}; !slices.Equal(sorted(pulsing), want) {
		t.Errorf("pulsing rows %v, want %v", pulsing, want)
	}
	var engagedWithChip, engagedNoChip []string
	for id := range engaged {
		if slices.Contains(chipped, id) {
			engagedWithChip = append(engagedWithChip, id)
		} else {
			engagedNoChip = append(engagedNoChip, id)
		}
	}
	if !slices.Equal(sorted(pulsing), sorted(engagedWithChip)) {
		t.Errorf("pulsing %v, engaged with a chip %v: want equal", pulsing, engagedWithChip)
	}
	if !slices.Equal(engagedNoChip, []string{"sto_placed"}) || !strings.Contains(row("sto_placed"), `<td class="col-reviews">—</td>`) {
		t.Errorf("engaged without a chip %v, want [sto_placed] with an empty Progress cell", engagedNoChip)
	}
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, want := range []string{
		`data-engagement-count="3"`, `title="engaged here: sto_disp, sto_placed, sto_seated"`,
		`data-elsewhere-count="2"`, `title="engaged elsewhere: sto_far, sto_node"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("header lacks %q", want)
		}
	}
	if r := row("sto_parked"); !strings.Contains(r, "stage-chip stage-current") || strings.Contains(r, "stage-performing") || strings.Contains(r, "data-engaged") {
		t.Errorf("the parked-at-step story must show a still current chip and no engagement:\n%s", r)
	}
	for _, id := range ids {
		for _, m := range chipClass.FindAllStringSubmatch(row(id), -1) {
			if !slices.Contains([]string{"stage-done", "stage-current", "stage-performing"}, m[1]) {
				t.Errorf("%s: chip class %q, want exactly one of done, current, performing", id, m[1])
			}
		}
		if slices.Contains(pulsing, id) && strings.Contains(row(id), "stage-current") {
			t.Errorf("%s pulses and also has a still current chip", id)
		}
	}

	// AC3: the hover says which, and for a pulsing chip where and by whom.
	titleOf := func(id, class string) string {
		m := regexp.MustCompile(`class="stage-chip ` + class + `" title="([^"]*)"`).FindStringSubmatch(row(id))
		if m == nil {
			t.Fatalf("%s has no %s chip", id, class)
		}
		return html.UnescapeString(m[1])
	}
	for id, want := range map[string][]string{
		"sto_seated": {"s2: performing (pa2)", "engaged here by bob, session s-user ("},
		"sto_disp":   {"s2: performing (pa2)", "engaged here by bob, session perform_ab12 ("},
		"sto_far":    {"s2: performing (pa2)", "engaged by ann on other-host (m-other), session s-far"},
		"sto_node":   {"s2: performing (pa2)", "engaged by bob at remote:nod_7, session s-node"},
	} {
		got := titleOf(id, "stage-performing")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: hover %q lacks %q", id, got, w)
			}
		}
	}
	if got := titleOf("sto_parked", "stage-current"); !strings.Contains(got, "s2: current, not being worked") || strings.Contains(got, "performing") || strings.Contains(got, "engaged") {
		t.Errorf("sto_parked: hover %q", got)
	}
}

// animatedClasses is the set of single-class .stage-* selectors css gives an
// animation, read from each rule's own body.
func animatedClasses(t *testing.T, css string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.(stage-[a-z-]+) \{([^}]*)\}`).FindAllStringSubmatch(css, -1) {
		if strings.Contains(m[2], "animation") {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("the served stylesheet animates no .stage-* class")
	}
	return out
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
