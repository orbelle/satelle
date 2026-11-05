package ledger

import (
	"encoding/json"
	"testing"
	"time"
)

// The canonical form documented in api/satelle/chain/v2/chain.proto: this test
// keeps that comment and Canonical in step.
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
