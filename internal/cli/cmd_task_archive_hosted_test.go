package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_0810c2a3: a task's archive event must build on the task chain's hosted
// head, or every later push refuses the task as moved and no pull clears it.

// publishedTask creates a task in a hosted-mode repo, gives it a comment, pushes
// it, then has another location append two events to its hosted chain. It
// returns the task and the hosted chain, now ahead of this checkout.
func publishedTask(t *testing.T) (f *fakeWorkstateServer, server, repo, task string, hostedChain []string) {
	t.Helper()
	return publishedItem(t, workitem.KindTask)
}

// publishedItem is publishedTask for an item of kind: a task, or an execution of
// a task created with it.
func publishedItem(t *testing.T, kind workitem.Kind) (f *fakeWorkstateServer, server, repo, task string, hostedChain []string) {
	t.Helper()
	ts, f, repo := hostedModeRepo(t)
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		in := workitem.CreateInput{Kind: kind, Title: "superseded"}
		if kind == workitem.KindExecution {
			parent, err := db.Stories.Create(ctx, workitem.CreateInput{Kind: workitem.KindTask, Title: "the task it runs"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			in.ParentID = parent.ID
		}
		it, err := db.Stories.Create(ctx, in, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		task = it.ID
	})
	appendLocal(t, task, "made here")
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	// The server's work-state page carries no story events, as the real one's does
	// not: a pull reads them only from the chain.
	f.mu.Lock()
	f.ledgerByID = map[string]map[string]any{}
	f.mu.Unlock()
	published := f.chain.stored("probe", task)
	if len(published) == 0 {
		t.Fatalf("the push published no event of %s", task)
	}
	now := time.Now().UTC()
	elsewhere := func(id, base string) *syncpb.Event {
		return hostedEvent(t, ledger.Entry{ID: id, StoryID: task, BaseID: base, Kind: ledger.KindComment, Actor: "alice", Body: "made elsewhere", CreatedAt: now})
	}
	f.chain.seed("probe", task, elsewhere("evt_else1", published[len(published)-1]), elsewhere("evt_else2", "evt_else1"))
	return f, ts.URL, repo, task, f.chain.stored("probe", task)
}

// assertPublished pushes and checks the push sent the task exactly one Append,
// carrying only the local events written past the hosted chain, built on the
// hosted latest, and that no conflict stands for the task.
func assertPublished(t *testing.T, f *fakeWorkstateServer, server, repo, task string, written []string, hostedLatest string) {
	t.Helper()
	sent := len(f.chain.appendsFor(task))
	out, err := pushRaw(t, server)
	if err != nil || strings.Contains(out, "moved since") {
		t.Fatalf("push = %v\n%s, want the archived task published", err, out)
	}
	appends := f.chain.appendsFor(task)[sent:]
	if len(appends) != 1 {
		t.Fatalf("push sent %d Append(s) for %s, want 1", len(appends), task)
	}
	if got := appends[0].GetBaseId(); got != hostedLatest {
		t.Errorf("the Append builds on %q, want the hosted latest %q", got, hostedLatest)
	}
	if got := eventIDs(appends[0].GetEvents()); !slices.Equal(got, written) {
		t.Errorf("the Append carried %v, want only the events written here %v", got, written)
	}
	if stored := f.chain.stored("probe", task); !slices.Equal(stored[len(stored)-len(written):], written) {
		t.Errorf("the hosted chain is %v, want it to end with %v", stored, written)
	}
	if c, ok := storyConflicts(t, repo)[task]; ok {
		t.Errorf("a conflict stands for %s after the push: %+v", task, c)
	}
}

// AC1: task archive reads the task's hosted chain first, so its event builds on
// the hosted latest and the next push publishes it.
func TestTaskArchiveBuildsOnHostedHead(t *testing.T) {
	f, server, repo, task, hostedChain := publishedTask(t)

	if out, err := runRoot(t, "task", "archive", task); err != nil {
		t.Fatalf("task archive: %v\n%s", err, out)
	}
	written := assertBuiltOnHostedHead(t, task, hostedChain)
	if len(written) != 1 {
		t.Fatalf("archive wrote %v, want one event", written)
	}
	chain := localChain(t, task)
	if last := chain[len(chain)-1]; last.Kind != ledger.KindTaskUpdated {
		t.Fatalf("the last local event is %s (%s), want the archive's %s", last.ID, last.Kind, ledger.KindTaskUpdated)
	}
	assertPublished(t, f, server, repo, task, written, hostedChain[len(hostedChain)-1])
}

// AC2: a task archived before this fix — on a local chain that never took its
// hosted events and with no saved head — is refused by push as moved, and one
// pull reads the task's chain so the next push publishes the archive. keep is how
// many hosted events the stale local chain still holds: none is the genesis an
// archive starts on an empty chain, and one is a shared prefix.
func TestPullMakesStaleArchivedTaskPublishable(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep int
	}{{"genesis", 0}, {"shared prefix", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			f, server, repo, task, hostedChain := publishedTask(t)
			archive := staleArchive(t, server, repo, task, tc.keep)

			out, err := pushRaw(t, server)
			if err == nil || !strings.Contains(out, "the story "+task+" moved since") {
				t.Fatalf("push of the stale archive = %v\n%s, want it refused as moved", err, out)
			}
			if _, ok := storyConflicts(t, repo)[task]; !ok {
				t.Fatalf("no conflict recorded for the refused %s", task)
			}

			if out, err := pullRaw(t, server); err != nil {
				t.Fatalf("pull: %v\n%s", err, out)
			}
			got := assertBuiltOnHostedHead(t, task, hostedChain)
			// The workflow_version the stale append recorded duplicates the hosted
			// chain's own and is dropped; only the archive is still to publish.
			if !slices.Equal(got, []string{archive}) {
				t.Fatalf("after the pull the local events past the hosted chain are %v, want only the archive %s", got, archive)
			}
			assertPublished(t, f, server, repo, task, got, hostedChain[len(hostedChain)-1])
		})
	}
}

// staleArchive puts the task in the state an archive made before this fix left
// it: only the first keep hosted events held locally, no saved head, and the
// archive event appended on the local tip. It returns the archive event's id.
func staleArchive(t *testing.T, server, repo, task string, keep int) string {
	t.Helper()
	return staleWrite(t, server, repo, task, keep, func(db *store.DB) string {
		ctx := context.Background()
		if _, err := db.Stories.Archive(ctx, task, time.Now()); err != nil {
			t.Fatal(err)
		}
		e, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: task, Kind: ledger.KindTaskUpdated, Actor: "satelle", Body: "archived"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return e.ID
	})
}

// staleWrite is staleArchive for any write: write appends it on the local tip
// and returns its event's id.
func staleWrite(t *testing.T, server, repo, task string, keep int, write func(db *store.DB) string) string {
	t.Helper()
	ctx := context.Background()
	var archive string
	withRepoStore(t, func(db *store.DB) {
		order, err := chainOrder(mustChain(t, db, task))
		if err != nil {
			t.Fatal(err)
		}
		var drop []string
		for _, e := range order[keep:] {
			drop = append(drop, e.ID)
		}
		if err := db.Ledger.RemoveEvents(ctx, task, drop, func(ledger.Entry) bool { return true }); err != nil {
			t.Fatal(err)
		}
		archive = write(db)
		stale, err := chainOrder(mustChain(t, db, task))
		if err != nil {
			t.Fatal(err)
		}
		// keep 0: the local chain starts a root of its own; otherwise it holds the
		// first keep hosted events and then only events the server lacks.
		if got := idsOf(stale[:keep]); !slices.Equal(got, idsOf(order[:keep])) {
			t.Fatalf("the stale chain starts %v, want the hosted prefix %v", got, idsOf(order[:keep]))
		}
		for _, e := range stale[keep:] {
			if slices.Contains(idsOf(order), e.ID) {
				t.Fatalf("the stale chain past the prefix holds the hosted event %s", e.ID)
			}
		}
	})
	cur, err := hosted.LoadWorkstateCursor(server, "probe", repo)
	if err != nil {
		t.Fatal(err)
	}
	delete(cur.StoryHeads, task)
	if cur.StoryHeads == nil {
		cur.StoryHeads = map[string]string{}
	}
	if err := hosted.SaveWorkstateCursor(server, "probe", repo, cur); err != nil {
		t.Fatal(err)
	}
	if _, ok := storyHeads(t, server, "probe", repo)[task]; ok {
		t.Fatalf("the head of %s is still saved", task)
	}
	return archive
}

func mustChain(t *testing.T, db *store.DB, id string) []ledger.Entry {
	t.Helper()
	events, err := db.Ledger.StoryChain(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
