package ledger

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCanonicalMatchesTheWireContract(t *testing.T) {
	e := Event{
		ID: "e1", Item: "sto_1a2b3c4d", Kind: "item.created", Actor: "ada",
		At:      time.Date(2026, 11, 5, 9, 23, 0, 0, time.UTC),
		Payload: json.RawMessage(`{ "b": [true, null], "a": 1 }`),
	}
	got, err := Canonical(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"actor":"ada","at":"2026-11-05T09:23:00Z","id":"e1","item":"sto_1a2b3c4d","kind":"item.created","payload":{"a":1,"b":[true,null]},"prev":""}`
	if string(got) != want {
		t.Fatalf("canonical =\n%s\nwant\n%s", got, want)
	}

	e.At = time.Date(2026, 11, 5, 9, 23, 0, 500_000_000, time.FixedZone("x", 3600))
	e.Payload = nil
	got, _ = Canonical(e)
	want = `{"actor":"ada","at":"2026-11-05T08:23:00.5Z","id":"e1","item":"sto_1a2b3c4d","kind":"item.created","payload":null,"prev":""}`
	if string(got) != want {
		t.Fatalf("empty payload canonical =\n%s\nwant\n%s", got, want)
	}

	e.Payload = json.RawMessage(`{"s":"<a&b> é\b"}`)
	got, _ = Canonical(e)
	want = `{"actor":"ada","at":"2026-11-05T08:23:00.5Z","id":"e1","item":"sto_1a2b3c4d","kind":"item.created","payload":{"s":"<a&b>\u2028é\b"},"prev":""}`
	if string(got) != want {
		t.Fatalf("string escaping canonical =\n%s\nwant\n%s", got, want)
	}
}

func TestShortHash(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123": "0123456789ab",
		"0123456789ab":         "0123456789ab",
		"abc":                  "abc",
		"":                     "",
	} {
		if got := ShortHash(in); got != want {
			t.Errorf("ShortHash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelink(t *testing.T) {
	at := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	link := func(e Event, prev string) Event {
		e.Prev = prev
		h, err := HashOf(e)
		if err != nil {
			t.Fatal(err)
		}
		e.Hash = h
		return e
	}
	head := link(Event{ID: "h", Item: "it", Kind: "note", At: at, Actor: "a"}, "")
	other := link(Event{ID: "o", Item: "it", Kind: "note", At: at, Actor: "b"}, "")
	e := link(Event{ID: "e1", Item: "it", Kind: "seat.released", At: at, Actor: "ada", Payload: json.RawMessage(`{"session":"s"}`)}, other.Hash)

	got, err := Relink(e, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChain("it", []Event{head, got}); err != nil {
		t.Fatalf("relinked event does not verify after its new prev: %v", err)
	}
	if got.ID != e.ID || !got.At.Equal(e.At) || got.Actor != e.Actor || got.Kind != e.Kind || string(got.Payload) != string(e.Payload) {
		t.Errorf("relink changed the event's content: %+v from %+v", got, e)
	}
	if got.Hash == e.Hash {
		t.Error("relinked hash is unchanged")
	}
	if _, err := Relink(Event{ID: "bad", Payload: json.RawMessage(`{`)}, head.Hash); err == nil {
		t.Error("an undecodable payload relinked")
	}
}
