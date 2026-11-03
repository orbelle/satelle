package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_937fd6cc: an execution's chain is published by the same compare-and-append
// as a task's, so a pull must read it as it reads a task's, and rechain must
// repair one whose hosted chain carries no status (exe_317cdf90).

// cancelEvent is the status_transition a local backlog → cancelled writes.
func cancelEvent(id string) ledger.Entry {
	return ledger.Entry{
		ID: ledger.NewID(), StoryID: id, Kind: ledger.KindStatusTransition, Actor: "bob",
		Body: "backlog → cancelled", Payload: json.RawMessage(`{"from":"backlog","to":"cancelled"}`),
	}
}

// AC1: an execution cancelled here on a chain that never took its hosted events
// is refused by push as moved; one pull reads the execution's chain, takes
// the hosted events and moves the cancel after them, so the next push publishes it.
func TestPullMakesExecutionChainPublishable(t *testing.T) {
	f, server, repo, exe, hostedChain := publishedItem(t, workitem.KindExecution)
	if !strings.HasPrefix(exe, "exe_") {
		t.Fatalf("publishedItem made %s, want an execution", exe)
	}
	// The cancel is written as an earlier release left it: on a local chain that
	// holds only the first hosted event, with no saved head.
	cancel := staleWrite(t, server, repo, exe, 1, func(db *store.DB) string {
		e := cancelEvent(exe)
		got, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
			StoryID: exe, Kind: e.Kind, Actor: e.Actor, Body: e.Body, Payload: e.Payload,
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return got.ID
	})

	out, err := pushRaw(t, server)
	if err == nil || !strings.Contains(out, exe+" moved since") {
		t.Fatalf("push of the lagging execution = %v\n%s, want it refused as moved", err, out)
	}

	if out, err := pullRaw(t, server); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	written := assertBuiltOnHostedHead(t, exe, hostedChain)
	if !slices.Equal(written, []string{cancel}) {
		t.Fatalf("after the pull the local events past the hosted chain are %v, want only the cancel %s", written, cancel)
	}
	assertPublished(t, f, server, repo, exe, written, hostedChain[len(hostedChain)-1])
}

// AC2: exe_317cdf90's shape — a published execution whose hosted chain holds no
// status event, cancelled here on a root of its own. rechain treats the empty
// hosted fold as the start: --dry-run reports the replay and changes nothing, the
// run replays the local work after the hosted latest, and the next push
// publishes it.
func TestRechainRepairsExecutionWithNoHostedStatus(t *testing.T) {
	f, server, repo, exe, hostedChain := publishedItem(t, workitem.KindExecution)
	own := relinked(
		ledger.Entry{ID: ledger.NewID(), StoryID: exe, Kind: ledger.KindComment, Actor: "bob", Body: "superseded"},
		cancelEvent(exe),
	)
	setLocalChain(t, exe, own)
	ownIDs := idsOf(own)

	out, err := pushRaw(t, server)
	if err == nil || !strings.Contains(out, exe+" moved since") {
		t.Fatalf("push of the forked execution = %v\n%s, want it refused as moved", err, out)
	}
	before := chainShape(localChain(t, exe))

	out, err = runRoot(t, "sync", "workstate", "rechain", exe, "--dry-run", "--server", server)
	if err != nil || !strings.Contains(out, "would rechain "+exe) || !strings.Contains(out, "replay 2 local event(s)") {
		t.Fatalf("rechain --dry-run = %v\n%s, want the two local events replayed", err, out)
	}
	if got := chainShape(localChain(t, exe)); !slices.Equal(got, before) {
		t.Fatalf("--dry-run changed the chain: %v, want %v", got, before)
	}
	out, err = runRoot(t, "sync", "workstate", "rechain", exe, "--server", server)
	if err != nil || !strings.Contains(out, "rechained "+exe) {
		t.Fatalf("rechain = %v\n%s", err, out)
	}
	if got := assertBuiltOnHostedHead(t, exe, hostedChain); !slices.Equal(got, ownIDs) {
		t.Fatalf("after rechain the local events past the hosted chain are %v, want the local work %v", got, ownIDs)
	}
	assertPublished(t, f, server, repo, exe, ownIDs, hostedChain[len(hostedChain)-1])
}
