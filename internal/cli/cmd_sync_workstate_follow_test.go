package cli

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// otherMachineEvent is an event another machine appended to id after its head,
// stored on the server and queued on the subscription.
func otherMachineEvent(t *testing.T, f *fakeWorkstateServer, id, body string, at time.Time) ledger.Entry {
	t.Helper()
	return otherMachineEntry(t, f, ledger.Entry{StoryID: id, Kind: ledger.KindComment, Actor: "alice", Body: body, CreatedAt: at})
}

// otherMachineTransition is a status transition another machine appended to id.
func otherMachineTransition(t *testing.T, f *fakeWorkstateServer, id, from, to string) ledger.Entry {
	t.Helper()
	p, err := json.Marshal(map[string]string{"from": from, "to": to})
	if err != nil {
		t.Fatal(err)
	}
	return otherMachineEntry(t, f, ledger.Entry{StoryID: id, Kind: ledger.KindStatusTransition, Actor: "alice", Body: from + " -> " + to, Payload: p, CreatedAt: time.Now().UTC()})
}

// otherMachineEntry chains e after id's stored head on the server and queues it on
// the subscription.
func otherMachineEntry(t *testing.T, f *fakeWorkstateServer, e ledger.Entry) ledger.Entry {
	t.Helper()
	stored := f.chain.stored("probe", e.StoryID)
	e.ID, e.BaseID = ledger.NewID(), stored[len(stored)-1]
	id := e.StoryID
	ev := hostedEvent(t, e)
	f.chain.seed("probe", id, ev)
	f.chain.mu.Lock()
	f.chain.feed = append(f.chain.feed, ev)
	f.chain.mu.Unlock()
	return e
}

func snapshotCalls(f *fakeWorkstateServer) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// sty_7c01ca14 AC4: outside team scope the follower never trims: an event for a
// non-performing story is applied and its chain kept, and a story whose history
// was left on the server is fetched whole on its next event.
func TestFollowNeverTrimsOutsideTeamScope(t *testing.T) {
	ts, f, repo := hostedModeRepo(t)
	useScope(t, kindPersonal)
	longAgo := time.Now().Add(-72 * time.Hour).UTC()
	idle := cacheStory(t, f, ts.URL, repo, "idle", "backlog", longAgo)
	remote := cacheStory(t, f, ts.URL, repo, "left remote by an earlier team scope", "backlog", longAgo)
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		if _, err := db.Ledger.DropStoryHistory(ctx, remote, storyHeads(t, ts.URL, "probe", repo)[remote]); err != nil {
			t.Fatal(err)
		}
		if err := db.Ledger.SetMarks(ctx, ledger.MarkHistoryRemote, []string{remote}); err != nil {
			t.Fatal(err)
		}
	})
	e := otherMachineEvent(t, f, idle, "an event on a story nobody performs", time.Now().UTC())
	otherMachineEvent(t, f, remote, "a comment", time.Now().UTC())

	out, err := runRoot(t, "sync", "workstate", "follow")
	if err != nil {
		t.Fatalf("follow: %v\n%s", err, out)
	}
	if got, want := localIDs(t, idle), f.chain.stored("probe", idle); !slices.Contains(got, e.ID) || !slices.Equal(got, want) {
		t.Errorf("the idle story holds %v, want its whole chain %v kept", got, want)
	}
	if got, want := localIDs(t, remote), f.chain.stored("probe", remote); !slices.Equal(got, want) || hasMark(t, remote, ledger.MarkHistoryRemote) {
		t.Errorf("the remote story holds %v, want its whole hosted chain %v fetched", got, want)
	}
}
