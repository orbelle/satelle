//go:build !windows

package kernel

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
)

// Every seat.bound records the kernel's machine and host, on the chain and
// through FoldSeats, for a seat command and for a dispatched run's seat
// (sto_e127b2e6 AC4).
func TestSeatRecordsTheMachine(t *testing.T) {
	k, _ := lineKernel(t, "", "")
	k.Machine, k.Host = "m1", "h1"
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(it.ID, "s1", LocalLocation(k.Dir), "", "ada"); err != nil {
		t.Fatal(err)
	}
	var during []item.Seat
	k.Dispatcher = probeDispatcher{probe: func(context.Context, agent.Request) (agent.Result, error) {
		events, _ := k.Ledger.Read(it.ID)
		during = item.FoldSeats(events)
		return agent.Result{Text: "did it"}, nil
	}}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	events, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound := 0
	for _, e := range events {
		if e.Kind != item.KindSeatBound {
			continue
		}
		bound++
		var s item.Seat
		if err := e.Decode(&s); err != nil || s.Machine != "m1" || s.Host != "h1" {
			t.Errorf("seat.bound payload %+v (%v), want machine m1 host h1", s, err)
		}
	}
	if bound != 2 {
		t.Fatalf("%d seat.bound events, want the seat's and the run's", bound)
	}
	if len(during) != 2 {
		t.Fatalf("seats during the run = %+v", during)
	}
	for _, s := range during {
		if s.Machine != "m1" || s.Host != "h1" {
			t.Errorf("folded seat %+v lacks the machine", s)
		}
	}
}

type fakeOwner struct{ calls *[]string }

func (f fakeOwner) Own(s string) error    { *f.calls = append(*f.calls, "own "+s); return nil }
func (f fakeOwner) Forget(s string) error { *f.calls = append(*f.calls, "forget "+s); return nil }

// A dispatched run owns its session before dispatch and forgets it after its
// seat is released (sto_e127b2e6 AC5).
func TestPerformLocalOwnsItsSession(t *testing.T) {
	k, _ := lineKernel(t, "", "")
	var calls []string
	k.Sessions = fakeOwner{&calls}
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	session := ""
	k.Dispatcher = probeDispatcher{probe: func(_ context.Context, req agent.Request) (agent.Result, error) {
		session, _ = envValue(req.Env, "SATELLE_SESSION")
		calls = append(calls, "dispatch")
		return agent.Result{Text: "did it"}, nil
	}}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	want := "own " + session + ",dispatch,forget " + session
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s, want %s", got, want)
	}
	if seats, _ := k.Cache.Seats(session, it.ID); len(seats) != 0 {
		t.Fatalf("the run's seat is still bound: %+v", seats)
	}
}
