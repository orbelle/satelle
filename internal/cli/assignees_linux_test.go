package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// seatUserAs binds session on id at this tree from a kernel stamping machine,
// host and user: a seat another user, or this user on another machine, bound.
func seatUserAs(t *testing.T, id, session, machine, host, user string) {
	t.Helper()
	k, r, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Machine, k.Host = machine, host
	k.User = func() string { return user }
	if _, err := k.Seat(id, session, kernel.LocalLocation(r.Tree), "", "test"); err != nil {
		t.Fatal(err)
	}
}

// Taking over, switching machines and pairing each show the right assignees
// without editing the field, and a released or proven-orphaned seat drops its
// user from the working set (sto_600b9a85 AC3, AC4).
func TestAssigneesFollowTheSeats(t *testing.T) {
	isolate(t)
	must(t, "init")
	home := config.GlobalDir()
	machine, err := config.MachineID(home)
	if err != nil {
		t.Fatal(err)
	}
	id := createID(t, "story", "create", "--title", "worked by many")
	live := func(session string) {
		t.Helper()
		if err := seatlife.Own(home, session, procgroup.Self()); err != nil {
			t.Fatal(err)
		}
	}
	const ada, bob, carol, dan = "ada@x.io", "bob@x.io", "carol@x.io", "dan@x.io"

	check := func(step string, users, working []string) {
		t.Helper()
		var got struct {
			Assignees []string `json:"assignees"`
			Working   []string `json:"working"`
		}
		if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &got); err != nil {
			t.Fatal(err)
		}
		if got.Assignees == nil || got.Working == nil {
			t.Errorf("%s: get --json lacks an array: %+v", step, got)
		}
		if !slices.Equal(got.Assignees, users) || !slices.Equal(got.Working, working) {
			t.Errorf("%s: get --json assignees %q working %q, want %q working %q", step, got.Assignees, got.Working, users, working)
		}
		var rows []struct {
			ID        string   `json:"id"`
			Assignees []string `json:"assignees"`
			Working   []string `json:"working"`
		}
		if err := json.Unmarshal([]byte(must(t, "story", "list", "--json")), &rows); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			found = true
			if !slices.Equal(r.Assignees, users) || !slices.Equal(r.Working, working) {
				t.Errorf("%s: list --json assignees %q working %q, want %q working %q", step, r.Assignees, r.Working, users, working)
			}
		}
		if !found {
			t.Errorf("%s: list --json lacks %s", step, id)
		}
		text := must(t, "story", "get", id)
		marked := make([]string, len(users))
		for i, u := range users {
			marked[i] = u
			if slices.Contains(working, u) {
				marked[i] += " (working)"
			}
		}
		if len(users) == 0 {
			if strings.Contains(text, "assignees") {
				t.Errorf("%s: text shows an assignees line with none:\n%s", step, text)
			}
		} else if want := "assignees  " + strings.Join(marked, ", ") + "\n"; !strings.Contains(text, want) {
			t.Errorf("%s: text lacks %q:\n%s", step, want, text)
		}
	}
	updates := func() int {
		n := 0
		for _, e := range chain(t, id) {
			if e.Kind == item.KindUpdated {
				n++
			}
		}
		return n
	}
	before := updates()

	// 1. Pairing: two users seated here, both live.
	seatUserAs(t, id, "s1", machine, "this-host", ada)
	live("s1")
	seatUserAs(t, id, "s2", machine, "this-host", bob)
	live("s2")
	check("pairing", []string{ada, bob}, []string{ada, bob})
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(it.Assignees, []string{ada, bob}) {
		t.Errorf("get --json read as an item: assignees %q, want [%s %s]", it.Assignees, ada, bob)
	}

	// 2. Switching machines: ada also seats on another machine, listed once.
	seatUserAs(t, id, "s3", "m-other", "h2", ada)
	check("second machine", []string{ada, bob}, []string{ada, bob})
	must(t, "story", "release", id, "--session", "s1")
	// Users are listed in the bind order of their live seats: ada's is now s3.
	check("switched machines", []string{bob, ada}, []string{bob, ada})

	// 3. Taking over: carol seats, and bob's session is proven gone.
	seatUserAs(t, id, "s4", machine, "this-host", carol)
	live("s4")
	if err := seatlife.Own(home, "s2", killedProc(t)); err != nil {
		t.Fatal(err)
	}
	check("taken over", []string{ada, carol}, []string{ada, carol})

	// 4. Release: no live seat holds a user.
	must(t, "story", "release", id, "--session", "s3")
	must(t, "story", "release", id, "--session", "s4")
	check("released", []string{}, []string{})
	if n := updates(); n != before {
		t.Errorf("the seats wrote %d item.updated events; the field was edited", n-before)
	}

	// 5. Explicit: dan is assigned, then works it, then stops.
	must(t, "story", "set", id, "--add-assignee", dan)
	check("assigned", []string{dan}, []string{})
	seatUserAs(t, id, "s5", machine, "this-host", dan)
	live("s5")
	check("assigned and seated", []string{dan}, []string{dan})
	must(t, "story", "release", id, "--session", "s5")
	check("assigned, released", []string{dan}, []string{})
}
