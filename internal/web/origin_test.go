package web

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestOriginDoesNotChangeWebReads(t *testing.T) {
	o := ledger.Origin{Machine: "m1", Role: ledger.RoleSync, Harness: "h1", Worktree: "/w"}
	plain := []Entry{
		{ID: "1", Kind: mirror.EventUpdated, Payload: json.RawMessage(`{"workflow":"delivery"}`)},
		{ID: "2", Kind: mirror.EventTransitioned, Payload: json.RawMessage(`{"from":"backlog","to":"plan"}`)},
		{ID: "3", Kind: mirror.EventPerformerRun, Payload: json.RawMessage(`{"state":"plan","model":"m","duration_ms":1200,"tokens_in":5,"tokens_out":7}`)},
		{ID: "4", Kind: mirror.EventGateRun, Payload: json.RawMessage(`{"transition":"plan->in_progress","result":"reject","duration_ms":30,"tokens_in":1,"tokens_out":2}`)},
		{ID: "5", Kind: mirror.EventUpdated, Payload: json.RawMessage(`{"title":"t"}`)},
		{ID: "6", Kind: mirror.EventTransitioned, Payload: json.RawMessage(`{"from":"plan","to":"done"}`)},
	}
	origined := make([]Entry, len(plain))
	for i, e := range plain {
		raw, err := ledger.WithOrigin(e.Payload, &o)
		if err != nil {
			t.Fatal(err)
		}
		e.Payload = raw
		origined[i] = e
	}
	for i := range plain {
		if !reflect.DeepEqual(eventChips(plain[i]), eventChips(origined[i])) {
			t.Fatalf("eventChips(%s) differs", plain[i].ID)
		}
		fa, ta, oka := transitionOf(plain[i])
		fb, tb, okb := transitionOf(origined[i])
		if fa != fb || ta != tb || oka != okb {
			t.Fatalf("transitionOf(%s) differs", plain[i].ID)
		}
		if placementAt(plain, i) != placementAt(origined, i) {
			t.Fatalf("placementAt(%d) differs", i)
		}
	}
	if !placementAt(origined, 1) || placementAt(origined, 5) {
		t.Fatal("fixture: placement not read")
	}
	if len(eventChips(origined[2])) == 0 {
		t.Fatal("fixture: no chips")
	}
}
