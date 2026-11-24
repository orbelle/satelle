package item

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

type pinnedEvent struct {
	id, kind string
	payload  any
}

func pinnedPayloads() []pinnedEvent {
	s := func(v string) *string { return &v }
	ss := func(v ...string) *[]string { return &v }
	return []pinnedEvent{
		{"e1", KindCreated, Fields{Kind: s("story"), Title: s("t"), Body: s("b"), Criteria: ss("c1"), Tags: ss("x"), Workflow: s("delivery"), Status: s("backlog")}},
		{"e2", KindUpdated, Fields{Title: s("t2"), Tags: ss("x", "y")}},
		{"e3", KindSeatBound, Seat{Session: "s1", Location: "local:/w", Base: "abc", Machine: "m", Host: "h", User: "u"}},
		{"e4", KindAttachmentAdd, Attachment{Name: "plan", SHA256: "00", Size: 3, MediaType: "text/markdown"}},
		{"e5", KindTransitioned, Move{From: "backlog", To: "plan"}},
		{"e6", KindSeatReleased, SeatRelease{Session: "s1"}},
		{"e7", KindQueueAdded, QueueEntry{Item: "sto_1", Cause: "c"}},
	}
}

func buildChain(t *testing.T, wrap func(json.RawMessage) json.RawMessage) []ledger.Event {
	t.Helper()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var out []ledger.Event
	prev := ""
	for i, p := range pinnedPayloads() {
		raw, err := json.Marshal(p.payload)
		if err != nil {
			t.Fatal(err)
		}
		if wrap != nil {
			raw = wrap(raw)
		}
		e, err := ledger.NewEventWith(p.id, at.Add(time.Duration(i)*time.Minute), "sto_pin", p.kind, "a", json.RawMessage(raw), prev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		prev = e.Hash
	}
	return out
}

var pinnedHashes = []string{
	"3e505eacbce6cfd2e5f8508f4ef31f2497b5164759fc5dcd7956805f3e4aefdc",
	"8f87c747df030ecb18a517fbee6d0560d19c4cbda8f7ed5ad627a01b1052cae1",
	"a2fd94651f959e3fbc79ba264f81388050e794012e6d899ca7f3e3cc9bda2cd6",
	"6945c760b3df47cf7d48634ad24198b8e49b747918eff86a5ccd58c181e3e6e4",
	"c12fa9465c8ece97a97c2f1f85825107799c16bec9dd8d1b72eedcacc2109270",
	"0e82527d0ca3ebbc9be701722eeff3671a4ace3cd845500209badfcb96871c78",
	"4a837360f3b072cd2f3462155b43730c8e7d526250f1b9a727a47a9f986732cf",
}

func TestPreOriginChainVerifiesAndFolds(t *testing.T) {
	evs := buildChain(t, nil)
	if err := ledger.VerifyChain("sto_pin", evs); err != nil {
		t.Fatal(err)
	}
	for i, e := range evs {
		if e.Hash != pinnedHashes[i] {
			t.Errorf("event %s hash %s, pinned %s", e.ID, e.Hash, pinnedHashes[i])
		}
	}
	it, err := Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	want := Item{ID: "sto_pin", Kind: "story", Title: "t2", Body: "b", Criteria: []string{"c1"}, Status: "plan", Tags: []string{"x", "y"}, Workflow: "delivery", CreatedAt: created, UpdatedAt: created.Add(4 * time.Minute)}
	if !reflect.DeepEqual(it, want) {
		t.Fatalf("fold %+v\nwant %+v", it, want)
	}
	if seats := FoldSeats(evs); len(seats) != 0 {
		t.Fatalf("seats %+v", seats)
	}
	if seats := FoldSeats(evs[:3]); len(seats) != 1 || seats[0] != (Seat{Item: "sto_pin", Session: "s1", Location: "local:/w", Base: "abc", BoundAt: created.Add(2 * time.Minute), Machine: "m", Host: "h", User: "u"}) {
		t.Fatalf("seats %+v", seats)
	}
	wantAtt := []Attachment{{Item: "sto_pin", Name: "plan", SHA256: "00", Size: 3, MediaType: "text/markdown", At: created.Add(3 * time.Minute), Actor: "a"}}
	if atts := FoldAttachments(evs); !reflect.DeepEqual(atts, wantAtt) {
		t.Fatalf("attachments %+v", atts)
	}
	q := FoldQueue(evs)
	if !reflect.DeepEqual(q.Explicit, []string{"sto_1"}) || q.Causes["sto_1"] != "c" || q.LastFire != nil {
		t.Fatalf("queue %+v", q)
	}
	if PriorStatus(evs) != "backlog" {
		t.Fatalf("prior status %q", PriorStatus(evs))
	}
}

func TestOriginDoesNotChangeAnyFold(t *testing.T) {
	o := ledger.Origin{Machine: "m1", Role: ledger.RoleJudge, Harness: "h1", Worktree: "/w"}
	plain := buildChain(t, nil)
	origined := buildChain(t, func(raw json.RawMessage) json.RawMessage {
		out, err := ledger.WithOrigin(raw, &o)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	for _, c := range [][]ledger.Event{plain, origined} {
		if err := ledger.VerifyChain("sto_pin", c); err != nil {
			t.Fatal(err)
		}
	}
	for i := range plain {
		if plain[i].Hash == origined[i].Hash {
			t.Fatalf("event %d carries no origin", i)
		}
	}
	for n := 1; n <= len(plain); n++ {
		a, b := plain[:n], origined[:n]
		ia, erra := Fold(a)
		ib, errb := Fold(b)
		if !reflect.DeepEqual(ia, ib) || (erra == nil) != (errb == nil) {
			t.Fatalf("Fold differs at %d: %+v / %+v", n, ia, ib)
		}
		if !reflect.DeepEqual(FoldSeats(a), FoldSeats(b)) {
			t.Fatalf("FoldSeats differs at %d", n)
		}
		if !reflect.DeepEqual(FoldAttachments(a), FoldAttachments(b)) {
			t.Fatalf("FoldAttachments differs at %d", n)
		}
		if !reflect.DeepEqual(FoldQueue(a), FoldQueue(b)) {
			t.Fatalf("FoldQueue differs at %d", n)
		}
		if PriorStatus(a) != PriorStatus(b) {
			t.Fatalf("PriorStatus differs at %d", n)
		}
	}
}
