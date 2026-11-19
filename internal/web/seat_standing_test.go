//go:build linux || darwin

package web

import (
	"encoding/json"
	"html"
	"maps"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

func TestSatelledClassesEverySeat(t *testing.T) {
	seats, home := fourSeatHome(t, "sto_here", "sto_far", "sto_gone", "sto_old")
	snap := activitySnapshot("beta")
	var rows []json.RawMessage
	for _, s := range seats {
		snap.Stories = append(snap.Stories, raw(mirror.Item{ID: s.Item, Kind: "story", Title: "T " + s.Item, Status: "beta", Workflow: "flow", CreatedAt: activityT0, UpdatedAt: activityT0}))
		rows = append(rows, raw(s))
	}
	snap.Seats = rows
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)

	byStory := map[string]seatlife.Standing{}
	standing := standingHere()
	for _, s := range seats {
		byStory[s.Item], _ = standing(s)
	}
	want := map[string]seatlife.Standing{"sto_here": seatlife.Here, "sto_far": seatlife.Elsewhere, "sto_gone": seatlife.Gone, "sto_old": seatlife.Unverified}
	if !maps.Equal(byStory, want) {
		t.Fatalf("standings %v, want %v", byStory, want)
	}
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, want := range []string{
		`data-engagement-count="2"`, `title="engaged here: sto_here, sto_old"`, ">2 engaged<",
		`data-elsewhere-count="1"`, `title="engaged elsewhere: sto_far"`, ">1 elsewhere<",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the engaged badge lacks %s", want)
		}
	}
	activity := activityRowsOf(t, activityPanel(t, page))
	for id, suffix := range map[string]string{
		"sto_here": "",
		"sto_far":  " · elsewhere (other-host)",
		"sto_gone": " · session gone",
		"sto_old":  " · unverified",
	} {
		row, ok := activity[id]
		if !ok {
			t.Fatalf("%s is not on Activity", id)
		}
		if suffix == "" {
			for _, s := range []string{"elsewhere", "session gone", "unverified"} {
				if strings.Contains(row, s) {
					t.Errorf("%s's row is marked %q: %s", id, s, row)
				}
			}
			continue
		}
		if !strings.Contains(row, html.EscapeString(suffix)) && !strings.Contains(row, suffix) {
			t.Errorf("%s's row lacks %q: %s", id, suffix, row)
		}
	}
}
