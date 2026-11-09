//go:build linux || darwin

package web

import (
	"encoding/json"
	"html"
	"maps"
	"os/exec"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// fourSeatHome is a satelle home (SATELLE_HOME) with this machine's id and
// the session records of four seats, one per standing, on the stories here,
// elsewhere, gone and unverified: here is this machine with its session owner
// alive; elsewhere another machine; gone this machine with its owner killed;
// unverified no machine recorded. It returns the pushed seat rows and the home.
func fourSeatHome(t *testing.T, here, elsewhere, gone, unverified string) ([]mirror.Seat, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	machine, err := config.MachineID(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := seatlife.Own(home, "s-here", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	dead := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := seatlife.Own(home, "s-gone", dead); err != nil {
		t.Fatal(err)
	}
	seat := func(id, session, m, host string) mirror.Seat {
		return mirror.Seat{ID: id + "/" + session, Item: id, Session: session, Location: "local:/repos/x", Machine: m, Host: host}
	}
	return []mirror.Seat{
		seat(here, "s-here", machine, "this-host"),
		seat(elsewhere, "s-elsewhere", "m-other", "other-host"),
		seat(gone, "s-gone", machine, "this-host"),
		seat(unverified, "s-unverified", "", ""),
	}, home
}

// satelled classes each of four seats: the one bound here with its session
// alive and the unverified one (no machine recorded) count as engaged here
// (the badge reads 2 engaged, sto_44a18f5b), the one on another machine as
// engaged elsewhere (1 elsewhere), and the gone one as neither; Activity marks
// the other three elsewhere (with the host), session gone and unverified
// (sto_e127b2e6 AC6, AC8 "remote-machine seat shown as elsewhere").
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
