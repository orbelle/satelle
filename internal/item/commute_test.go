package item

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

var commuteAt = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

func ev(t *testing.T, kind string, payload any) ledger.Event {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Event{ID: kind, Item: "q", Kind: kind, At: commuteAt, Actor: "me", Payload: b}
}

func raw(kind, payload string) ledger.Event {
	return ledger.Event{ID: kind, Item: "q", Kind: kind, At: commuteAt, Actor: "me", Payload: json.RawMessage(payload)}
}

func queueCreated(t *testing.T, inputs *Selection) ledger.Event {
	kind, title := QueueKind, "q"
	return ev(t, KindCreated, Fields{Kind: &kind, Title: &title, Trigger: &Trigger{Workflow: "triage"}, Inputs: inputs})
}

func released(t *testing.T, s string) ledger.Event {
	return ev(t, KindSeatReleased, SeatRelease{Session: s})
}

func bound(t *testing.T, s string) ledger.Event {
	return ev(t, KindSeatBound, Seat{Session: s, Location: "local:/x"})
}

func added(t *testing.T, id, cause string) ledger.Event {
	return ev(t, KindQueueAdded, QueueEntry{Item: id, Cause: cause})
}

func removed(t *testing.T, id string) ledger.Event {
	return ev(t, KindQueueRemoved, QueueEntry{Item: id})
}

func fired(t *testing.T, kept ...string) ledger.Event {
	return ev(t, KindQueueFired, QueueFire{Run: "exe_1", Workflow: "triage", Kept: kept})
}

func moved(t *testing.T, from, to string) ledger.Event {
	return ev(t, KindTransitioned, Move{From: from, To: to})
}

func cat(parts ...[]ledger.Event) []ledger.Event { return slices.Concat(parts...) }

func one(e ...ledger.Event) []ledger.Event { return e }

func sameQueue(a, b QueueState) bool {
	x, y := slices.Clone(a.Explicit), slices.Clone(b.Explicit)
	slices.Sort(x)
	slices.Sort(y)
	if !slices.Equal(x, y) || !maps.Equal(a.Causes, b.Causes) || !maps.Equal(a.added, b.added) {
		return false
	}
	return len(a.Fires) == len(b.Fires)
}

func TestCommutesAllowlist(t *testing.T) {
	op := ev(t, KindAttachmentAdd, Attachment{Name: "n", SHA256: "x", Operator: true})
	att := ev(t, KindAttachmentAdd, Attachment{Name: "n", SHA256: "x"})
	upd := ev(t, KindUpdated, Fields{Title: new(string)})
	gate := ev(t, "gate.run", map[string]string{"gate": "g", "verdict": "accept"})

	seatPrefix := cat(one(queueCreated(t, nil)), one(bound(t, "S"), bound(t, "T")))
	for name, s := range map[string]ledger.Event{
		"release S": released(t, "S"), "release T": released(t, "T"), "bind T": bound(t, "T"),
		"transition": moved(t, "a", "b"), "update": upd, "gate": gate,
		"operator attachment": op, "attachment": att, "fire": fired(t),
	} {
		l := released(t, "S")
		ok, err := Commutes(seatPrefix, l, s)
		if err != nil || !ok {
			t.Errorf("seat.released{S} beside %s: %v %v", name, ok, err)
			continue
		}
		if a, b := FoldSeats(cat(seatPrefix, one(l, s))), FoldSeats(cat(seatPrefix, one(s, l))); !slices.Equal(a, b) {
			t.Errorf("seat.released{S} beside %s folds %v one way and %v the other", name, a, b)
		}
	}

	queuePrefix := one(queueCreated(t, nil), added(t, "x", "c0"))
	for name, s := range map[string]ledger.Event{
		"add other": added(t, "id2", "c"), "add same": added(t, "id", "c"), "remove other": removed(t, "x"),
		"bind": bound(t, "S"), "release": released(t, "S"),
	} {
		l := added(t, "id", "c")
		ok, err := Commutes(queuePrefix, l, s)
		if err != nil || !ok {
			t.Errorf("queue.added{id} beside %s: %v %v", name, ok, err)
			continue
		}
		if a, b := FoldQueue(cat(queuePrefix, one(l, s))), FoldQueue(cat(queuePrefix, one(s, l))); !sameQueue(a, b) {
			t.Errorf("queue.added{id} beside %s folds %+v one way and %+v the other", name, a, b)
		}
	}
}

func judge(t *testing.T, before []ledger.Event, newer []ledger.Event, l ledger.Event) (bool, error) {
	t.Helper()
	for j, s := range newer {
		ok, err := Commutes(cat(before, newer[:j]), l, s)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func TestCommutesQueueAddAfterFire(t *testing.T) {
	created := queueCreated(t, nil)
	withX := one(created, added(t, "x", "c"))

	t.Run("new id is re-based", func(t *testing.T) {
		fire, l := fired(t), added(t, "y", "c")
		ok, err := Commutes(withX, l, fire)
		if err != nil || !ok {
			t.Fatalf("Commutes %v %v", ok, err)
		}
		after, rebased := FoldQueue(cat(withX, one(fire))), FoldQueue(cat(withX, one(fire, l)))
		if len(after.Fires) != len(rebased.Fires) || after.LastFire.Run != rebased.LastFire.Run || !slices.Equal(after.LastFire.Kept, rebased.LastFire.Kept) {
			t.Errorf("the add changed the fire: %+v vs %+v", after, rebased)
		}
		if !slices.Equal(rebased.Explicit, []string{"y"}) || rebased.Causes["y"] != "c" {
			t.Errorf("rebased queue %+v", rebased)
		}
	})

	for _, tc := range []struct {
		name   string
		before []ledger.Event
		newer  []ledger.Event
		local  ledger.Event
	}{
		{"same id claimed", withX, one(fired(t)), added(t, "x", "c")},
		{"same id kept", withX, one(fired(t, "x")), added(t, "x", "c2")},
		{"kept id not explicit", one(created), one(fired(t, "x")), added(t, "x", "c")},
		{"filtered queue", one(queueCreated(t, &Selection{Kind: "story"})), one(fired(t)), added(t, "y", "c")},
		{"filter set by update", one(created, ev(t, KindUpdated, Fields{Inputs: &Selection{Tag: "t"}})), one(fired(t)), added(t, "y", "c")},
		{"add then fire on the store", one(created), one(added(t, "x", "c"), fired(t)), added(t, "x", "c")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := judge(t, tc.before, tc.newer, tc.local)
			if err != nil || ok {
				t.Fatalf("judged %v %v, want false", ok, err)
			}
		})
	}

	t.Run("add then fire pairs", func(t *testing.T) {
		add, l := added(t, "x", "c"), added(t, "x", "c")
		if ok, err := Commutes(one(created), l, add); err != nil || !ok {
			t.Fatalf("add beside add %v %v", ok, err)
		}
		if ok, err := Commutes(one(created, add), l, fired(t)); err != nil || ok {
			t.Fatalf("add beside the fire that claimed it %v %v", ok, err)
		}
	})

	t.Run("undecodable fire", func(t *testing.T) {
		if _, err := Commutes(withX, added(t, "y", "c"), raw(KindQueueFired, `{"kept":7}`)); err == nil {
			t.Fatal("an undecodable fire did not error")
		}
	})
}

func TestCommutesRefuses(t *testing.T) {
	prefix := one(queueCreated(t, nil), bound(t, "S"), added(t, "id", "c"))
	store := moved(t, "plan", "in_progress")
	l := added(t, "id", "c")
	for _, tc := range []struct {
		name     string
		local    ledger.Event
		store    ledger.Event
		wantsErr bool
	}{
		{"transition, matching from", moved(t, "backlog", "plan"), ev(t, KindUpdated, Fields{}), false},
		{"transition, mismatched from", moved(t, "plan", "blocked"), store, false},
		{"gate.run", ev(t, "gate.run", map[string]string{"gate": "g"}), store, false},
		{"seat.bound", bound(t, "T"), released(t, "S"), false},
		{"item.updated", ev(t, KindUpdated, Fields{}), store, false},
		{"operator attachment", ev(t, KindAttachmentAdd, Attachment{Name: "n", Operator: true}), store, false},
		{"attachment", ev(t, KindAttachmentAdd, Attachment{Name: "n"}), store, false},
		{"queue.fired", fired(t), released(t, "S"), false},
		{"queue.removed", removed(t, "id"), released(t, "S"), false},
		{"branch.collected", ev(t, "branch.collected", map[string]string{}), released(t, "S"), false},
		{"unknown kind", ev(t, "mystery", map[string]string{}), released(t, "S"), false},
		{"add past transition", l, store, false},
		{"add past update", l, ev(t, KindUpdated, Fields{}), false},
		{"add past gate.run", l, ev(t, "gate.run", map[string]string{}), false},
		{"add past attachment", l, ev(t, KindAttachmentAdd, Attachment{Name: "n"}), false},
		{"add past branch.collected", l, ev(t, "branch.collected", map[string]string{}), false},
		{"add past add of another cause", l, added(t, "id", "c2"), false},
		{"add past remove of it", l, removed(t, "id"), false},
		{"release past bind of the session", released(t, "S"), bound(t, "S"), false},
		{"undecodable release", raw(KindSeatReleased, `{"session":1}`), store, true},
		{"undecodable store add", l, raw(KindQueueAdded, `{"item":1}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := Commutes(prefix, tc.local, tc.store)
			if ok {
				t.Fatal("commutes")
			}
			if (err != nil) != tc.wantsErr {
				t.Fatalf("error %v, want one: %v", err, tc.wantsErr)
			}
		})
	}
}
