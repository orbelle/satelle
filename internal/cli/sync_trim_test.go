package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// hostedToml binds the repo explicitly.
const hostedToml = "[sync]\nhold_unpushed = false\nproject = \"probe\"\n"

// hostedModeRepo is a repo in hosted mode (sty_0dc600e1): the user is signed in to
// the fake server, which is the machine's hosted server, and [sync] project is set.
func hostedModeRepo(t *testing.T) (*httptest.Server, *fakeWorkstateServer, string) {
	t.Helper()
	ts, f := newFakeWorkstateServer(t)
	repo := workstateRepo(t, hostedToml)
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}
	seedCredAs(t, ts.URL, "usr_me")
	return ts, f, repo
}

// hostedTeamRepo is hostedModeRepo whose binding resolves to a project of a team
// the account is in: the one scope whose local store trims story history
// (sty_7c01ca14).
func hostedTeamRepo(t *testing.T) (*httptest.Server, *fakeWorkstateServer, string) {
	t.Helper()
	ts, f, repo := hostedModeRepo(t)
	useScope(t, kindTeam)
	return ts, f, repo
}

// cacheStory writes a story this checkout already published: its row and events,
// made at at, in the local store and on the fake server, with its head saved.
func cacheStory(t *testing.T, f *fakeWorkstateServer, server, repo, title, status string, at time.Time, extra ...ledger.Entry) string {
	t.Helper()
	return cacheItem(t, f, server, repo, workitem.CreateInput{Title: title, Status: status}, at, extra...)
}

// cacheItem is cacheStory for a story created from in, so a test can give it a
// category and tags.
func cacheItem(t *testing.T, f *fakeWorkstateServer, server, repo string, in workitem.CreateInput, at time.Time, extra ...ledger.Entry) string {
	t.Helper()
	ctx := context.Background()
	title, status := in.Title, in.Status
	in.Kind = workitem.KindStory
	var id string
	withRepoStore(t, func(db *store.DB) {
		it, err := db.Stories.Create(ctx, in, at)
		if err != nil {
			t.Fatal(err)
		}
		id = it.ID
		events := append([]ledger.Entry{
			{ID: ledger.NewID(), Kind: ledger.KindDefinitionSnapshot, Actor: "satelle", Body: "definition", Payload: storyfold.SnapshotPayloadOf(it, true)},
			{ID: ledger.NewID(), Kind: ledger.KindComment, Actor: "bob", Body: "one"},
			{ID: ledger.NewID(), Kind: ledger.KindComment, Actor: "bob", Body: "two"},
		}, extra...)
		base := ""
		var sent []*syncpb.Event
		for _, e := range events {
			e.StoryID, e.BaseID = id, base
			if e.CreatedAt.IsZero() {
				e.CreatedAt = at
			}
			if _, err := db.Ledger.Upsert(ctx, e, at); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, hostedEvent(t, e))
			base = e.ID
		}
		f.chain.seed("probe", id, sent...)
		if err := hosted.SaveStoryHeads(server, "probe", repo, map[string]string{id: base}); err != nil {
			t.Fatal(err)
		}
	})
	seedHostedStory(f, id, title, status, at)
	return id
}

// hostedEvent is e as Chain/Append carries it.
func hostedEvent(t *testing.T, e ledger.Entry) *syncpb.Event {
	t.Helper()
	raw, err := marshalWorkstateLedger(e)
	if err != nil {
		t.Fatal(err)
	}
	return &syncpb.Event{Id: e.ID, StoryId: e.StoryID, BaseId: e.BaseID, WorkflowVersion: e.WorkflowHash, Payload: raw}
}

// workingOnAt is the working_on event a location writes when it engages a story.
func workingOnAt(t *testing.T, location string) ledger.Entry {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"location": location, "state": "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Entry{ID: ledger.NewID(), Kind: ledger.KindWorkingOn, Actor: "executor", Body: "working on", Payload: payload}
}

// localEvents is how many events the local store holds for a story.
func localEvents(t *testing.T, id string) int {
	t.Helper()
	var n int
	withRepoStore(t, func(db *store.DB) {
		events, err := db.Ledger.StoryChain(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		n = len(events)
	})
	return n
}

func hasMark(t *testing.T, id, mark string) bool {
	t.Helper()
	var ok bool
	withRepoStore(t, func(db *store.DB) {
		var err error
		if ok, err = db.Ledger.HasMark(context.Background(), id, mark); err != nil {
			t.Fatal(err)
		}
	})
	return ok
}

func hasRow(t *testing.T, id string) bool {
	t.Helper()
	var ok bool
	withRepoStore(t, func(db *store.DB) {
		_, err := db.Stories.Get(context.Background(), id)
		ok = err == nil
	})
	return ok
}

// cacheFixture is the four stories the hold rule tells apart (sty_7c01ca14 AC1).
type cacheFixture struct {
	engaged, recent, old, performing string
}

func seedCacheFixture(t *testing.T, f *fakeWorkstateServer, server, repo string) cacheFixture {
	t.Helper()
	loc, err := hosted.LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	longAgo, now := time.Now().Add(-72*time.Hour).UTC(), time.Now().UTC()
	return cacheFixture{
		engaged:    cacheStory(t, f, server, repo, "engaged here", "backlog", longAgo, workingOnAt(t, loc)),
		recent:     cacheStory(t, f, server, repo, "recent", "backlog", now),
		old:        cacheStory(t, f, server, repo, "old", "backlog", longAgo),
		performing: cacheStory(t, f, server, repo, "performing elsewhere", "in_progress", longAgo, workingOnAt(t, "loc_elsewhere")),
	}
}

// sty_7c01ca14 AC4: outside team scope a snapshot leaves every chain on the local
// store: a personal-scope pass reads every hosted chain and marks nothing remote,
// and a local-scope pass moves nothing.
func TestNonTeamSnapshotPullsEveryChain(t *testing.T) {
	for _, kind := range []string{kindPersonal, kindForeign} {
		t.Run(kind, func(t *testing.T) {
			ts, f, repo := hostedModeRepo(t)
			useScope(t, kind)
			fx := seedCacheFixture(t, f, ts.URL, repo)

			if out, err := runRoot(t, "sync", "workstate", "snapshot"); err != nil {
				t.Fatalf("snapshot: %v\n%s", err, out)
			}
			for _, id := range []string{fx.performing, fx.engaged, fx.recent, fx.old} {
				if n, want := localEvents(t, id), len(f.chain.stored("probe", id)); n != want {
					t.Errorf("%s holds %d event(s), want all %d", id, n, want)
				}
				if hasMark(t, id, ledger.MarkHistoryRemote) {
					t.Errorf("%s is marked remote outside team scope", id)
				}
				if kind == kindPersonal && f.chain.readCount(id) == 0 {
					t.Errorf("the personal-scope snapshot did not read the hosted chain of %s", id)
				}
			}
		})
	}
}

// trimUnder runs one trim pass with scope recorded for the repo's binding — none
// recorded when scope is nil — and returns how many stories it dropped.
func trimUnder(t *testing.T, server, repo string, scope *config.Scope) int {
	t.Helper()
	if scope != nil {
		if err := syncstate.RecordResolvedScope(config.GlobalDir(), repo, *scope, "probe", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	// A command run first indexes the workflows the performing rule reads.
	if out, err := runRoot(t, "story", "list"); err != nil {
		t.Fatalf("story list: %v\n%s", err, out)
	}
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Wired as a command's bootstrap wires them: the indexed workflows, and the
	// signed-in holder hosted mode is judged from.
	withVerbWiring(t)
	verb.SetDocIndexStore(a.Store.DocIndex)
	verb.SetAssigneeResolver(func() string { return resolveUser(a.Config, a.RepoRoot).Holder() })
	n, err := trimHostedHistory(context.Background(), a, server, "probe")
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	return n
}
