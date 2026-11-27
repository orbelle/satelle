//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func terminalKernel(t *testing.T) (*Kernel, *fakeDispatcher, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"rel\"\n[items]\nprefix = { thing = \"th\" }\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.r1]\ncommand = \"claude\"\nmodel = \"m1\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "workflows", "rel.toml"), `
[states.a]
start = true
[states.b]
[states.blocked]
park = true
[states.done]
terminal = true
[states.cancelled]
terminal = true

[gates.review]
skill = "review"
agent = "r1"

[[transition]]
from = ["a"]
to = "b"
[[transition]]
from = ["a"]
to = "blocked"
[[transition]]
from = ["b"]
to = "done"
groups = [{ mode = "sequential", gates = ["review"] }]
[[transition]]
from = ["*"]
to = "cancelled"
[[transition]]
from = ["blocked"]
to = "cancelled"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept}}
	k.Dispatcher = d
	k.Git = fakeGit{}
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("finish it")}, "op")
	if err != nil {
		t.Fatal(err)
	}
	return k, d, it.ID
}

func seatAround(t *testing.T, k *Kernel, id string) []string {
	t.Helper()
	if _, err := k.Seat(id, "s1", LocalLocation(k.Dir), "", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(id, "s2", item.LocationRemote+"node-1", "", "s2"); err != nil {
		t.Fatal(err)
	}
	machine := k.Machine
	k.Machine = "m-other"
	if _, err := k.Seat(id, "s3", LocalLocation(k.Dir), "", "s3"); err != nil {
		t.Fatal(err)
	}
	k.Machine = machine
	return []string{"s1", "s2", "s3"}
}

func chainOf(t *testing.T, k *Kernel, id string) []ledger.Event {
	t.Helper()
	evs, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func assertAllReleased(t *testing.T, k *Kernel, id string, res TransitionResult, sessions []string) {
	t.Helper()
	if !reflect.DeepEqual(res.Released, sessions) {
		t.Fatalf("Released = %v, want %v", res.Released, sessions)
	}
	evs := chainOf(t, k, id)
	if left := item.FoldSeats(evs); len(left) != 0 {
		t.Fatalf("seats still bound after the terminal move: %+v", left)
	}
	tail := evs[len(evs)-len(sessions)-1:]
	if tail[0].Kind != item.KindTransitioned {
		t.Fatalf("expected item.transitioned before the releases, got %s", tail[0].Kind)
	}
	for i, s := range sessions {
		e := tail[i+1]
		var rel item.SeatRelease
		if e.Kind != item.KindSeatReleased || e.Actor != "me" || e.Decode(&rel) != nil || rel.Session != s {
			t.Fatalf("event %d after the move = %s by %s (%+v), want seat.released of %s by me", i, e.Kind, e.Actor, rel, s)
		}
	}
	for _, s := range sessions {
		if seats, err := k.Cache.Seats(s, ""); err != nil || len(seats) != 0 {
			t.Fatalf("cache still lists seats of %s: %+v %v", s, seats, err)
		}
	}
	if res.Item.Status != res.To {
		t.Fatalf("Item.Status = %s, want %s", res.Item.Status, res.To)
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}

func releases(evs []ledger.Event) int {
	n := 0
	for _, e := range evs {
		if e.Kind == item.KindSeatReleased {
			n++
		}
	}
	return n
}

func TestTerminalMoveReleasesEverySeat(t *testing.T) {
	k, _, id := terminalKernel(t)
	sessions := seatAround(t, k, id)
	move(t, k, id, "b")
	res := move(t, k, id, "done")
	assertAllReleased(t, k, id, res, sessions)
}

func TestCancelReleasesEverySeat(t *testing.T) {
	k, _, id := terminalKernel(t)
	sessions := seatAround(t, k, id)
	move(t, k, id, "b")
	res := move(t, k, id, "cancelled")
	assertAllReleased(t, k, id, res, sessions)
}

func TestCancelFromParkReleasesEverySeat(t *testing.T) {
	k, _, id := terminalKernel(t)
	sessions := seatAround(t, k, id)
	move(t, k, id, "blocked")
	res := move(t, k, id, "cancelled")
	assertAllReleased(t, k, id, res, sessions)
}

func TestNonTerminalMoveKeepsSeats(t *testing.T) {
	k, _, id := terminalKernel(t)
	seatAround(t, k, id)
	res := move(t, k, id, "b")
	if len(res.Released) != 0 {
		t.Fatalf("a non-terminal move released %v", res.Released)
	}
	evs := chainOf(t, k, id)
	if n := len(item.FoldSeats(evs)); n != 3 {
		t.Fatalf("got %d seats after a non-terminal move, want 3", n)
	}
	if n := releases(evs); n != 0 {
		t.Fatalf("a non-terminal move appended %d seat.released", n)
	}
}

func TestRefusedTerminalMoveKeepsSeats(t *testing.T) {
	k, d, id := terminalKernel(t)
	seatAround(t, k, id)
	move(t, k, id, "b")
	d.set("r1", reject)
	res, err := k.Transition(context.Background(), id, "done", "me")
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved || len(res.Released) != 0 {
		t.Fatalf("a refused move: Moved=%v Released=%v", res.Moved, res.Released)
	}
	evs := chainOf(t, k, id)
	if n := len(item.FoldSeats(evs)); n != 3 {
		t.Fatalf("got %d seats after a refused move, want 3", n)
	}
	if n := releases(evs); n != 0 {
		t.Fatalf("a refused move appended %d seat.released", n)
	}
}

func TestTerminalMoveReportsAFailedRelease(t *testing.T) {
	k, _, id := terminalKernel(t)
	seatAround(t, k, id)
	move(t, k, id, "b")
	k.Ledger.Now = func() time.Time {
		if b, err := os.ReadFile(k.Ledger.Path(id)); err == nil && strings.Contains(string(b), `"to":"cancelled"`) {
			k.Ledger.SetReadOnly("refused after the move")
		}
		return time.Now()
	}
	res, err := k.Transition(context.Background(), id, "cancelled", "me")
	if err == nil || !strings.Contains(err.Error(), "satelle doctor --resolve releases it") || !strings.Contains(err.Error(), "session s1") {
		t.Fatalf("want the failed release named, got %v", err)
	}
	if !res.Moved || res.Item.Status != "cancelled" || len(res.Released) != 0 {
		t.Fatalf("the move must stand: Moved=%v Status=%s Released=%v", res.Moved, res.Item.Status, res.Released)
	}
	evs := chainOf(t, k, id)
	if last := evs[len(evs)-1]; last.Kind != item.KindTransitioned {
		t.Fatalf("the move is not on the chain: last = %s", last.Kind)
	}
	if n := len(item.FoldSeats(evs)); n != 3 {
		t.Fatalf("got %d seats, want the 3 left for the backstop", n)
	}
}
