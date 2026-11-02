package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
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

// sty_7c01ca14 AC1: after a snapshot sync in team scope only the story whose
// folded status is performing keeps its history, though another location engaged
// it; the one this machine engaged, the recent one and the old one are kept as
// rows, marked for an on-demand fetch, and their hosted chains are not read.
func TestTeamSnapshotHoldsOnlyPerformingHistory(t *testing.T) {
	ts, f, repo := hostedTeamRepo(t)
	fx := seedCacheFixture(t, f, ts.URL, repo)

	if out, err := runRoot(t, "sync", "workstate", "snapshot"); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if n := localEvents(t, fx.performing); n == 0 {
		t.Errorf("the performing story %s lost its history", fx.performing)
	}
	if f.chain.readCount(fx.performing) == 0 {
		t.Errorf("the snapshot did not read the hosted chain of performing story %s", fx.performing)
	}
	for _, id := range []string{fx.engaged, fx.recent, fx.old} {
		if n := localEvents(t, id); n != 0 {
			t.Errorf("%s kept %d event(s), want its history left on the server", id, n)
		}
		if !hasMark(t, id, ledger.MarkHistoryRemote) {
			t.Errorf("%s is not marked for an on-demand fetch", id)
		}
		if n := f.chain.readCount(id); n != 0 {
			t.Errorf("the snapshot read the hosted chain of %s %d time(s), want none", id, n)
		}
	}
	for _, id := range []string{fx.performing, fx.engaged, fx.recent, fx.old} {
		if !hasRow(t, id) {
			t.Errorf("%s lost its row", id)
		}
	}
	if got := len(f.chain.stored("probe", fx.old)); got != 3 {
		t.Errorf("the server holds %d event(s) of the trimmed story, want 3", got)
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

// sty_7c01ca14 AC1, AC4: a trim pass in team scope keeps the chain of a story
// another location performs and drops one that location finished; in personal
// scope, local scope, or with no scope recorded it drops nothing.
func TestTrimHoldsPerformingStoriesOnlyInTeamScope(t *testing.T) {
	team, personal, local := config.TeamScope, config.PersonalScope, config.LocalScope
	for name, tc := range map[string]struct {
		scope *config.Scope
		drops bool
	}{
		"team": {&team, true}, "personal": {&personal, false}, "local": {&local, false}, "unrecorded": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			ts, f, repo := hostedModeRepo(t)
			longAgo := time.Now().Add(-72 * time.Hour).UTC()
			working := cacheStory(t, f, ts.URL, repo, "performed elsewhere", "in_progress", longAgo, workingOnAt(t, "loc_elsewhere"))
			finished := cacheStory(t, f, ts.URL, repo, "finished elsewhere", "done", longAgo, workingOnAt(t, "loc_elsewhere"))

			n := trimUnder(t, ts.URL, repo, tc.scope)
			if localEvents(t, working) == 0 {
				t.Errorf("the performing story lost its history")
			}
			if tc.drops {
				if n != 1 || localEvents(t, finished) != 0 || !hasMark(t, finished, ledger.MarkHistoryRemote) {
					t.Errorf("dropped %d; the finished story holds %d event(s), want it alone dropped and marked remote", n, localEvents(t, finished))
				}
				return
			}
			if n != 0 || localEvents(t, finished) == 0 || hasMark(t, finished, ledger.MarkHistoryRemote) {
				t.Errorf("dropped %d outside team scope; the finished story holds %d event(s), want every chain kept", n, localEvents(t, finished))
			}
		})
	}
}

// sty_7c01ca14 AC2: in team scope, opening a story whose history is not held
// (story get, or the fetch verb the web story page runs) fetches its chain, and the
// next snapshot drops it again unless another location has moved it into work by
// then.
func TestFetchedHistoryIsTrimmedAgainUnlessPerforming(t *testing.T) {
	ts, f, repo := hostedTeamRepo(t)
	longAgo := time.Now().Add(-72 * time.Hour).UTC()
	idle := cacheStory(t, f, ts.URL, repo, "opened and left", "backlog", longAgo)
	pickedUp := cacheStory(t, f, ts.URL, repo, "opened then picked up", "backlog", longAgo)
	if out, err := runRoot(t, "sync", "workstate", "snapshot"); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	for _, id := range []string{idle, pickedUp} {
		if localEvents(t, id) != 0 || !hasMark(t, id, ledger.MarkHistoryRemote) {
			t.Fatalf("%s was not trimmed by the snapshot", id)
		}
	}
	if out, err := runRoot(t, "story", "get", idle); err != nil {
		t.Fatalf("story get: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "sync", "workstate", "fetch", pickedUp); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	for _, id := range []string{idle, pickedUp} {
		if n, want := localEvents(t, id), len(f.chain.stored("probe", id)); n != want || hasMark(t, id, ledger.MarkHistoryRemote) {
			t.Fatalf("opening %s left %d of %d event(s) local", id, n, want)
		}
	}

	otherMachineTransition(t, f, pickedUp, "backlog", "in_progress")
	seedHostedStory(f, pickedUp, "opened then picked up", "in_progress", time.Now().UTC())
	if out, err := runRoot(t, "sync", "workstate", "snapshot"); err != nil {
		t.Fatalf("second snapshot: %v\n%s", err, out)
	}
	if n := localEvents(t, idle); n != 0 || !hasMark(t, idle, ledger.MarkHistoryRemote) {
		t.Errorf("the opened story nobody performs kept %d event(s), want it dropped again", n)
	}
	if got, want := localIDs(t, pickedUp), f.chain.stored("probe", pickedUp); !slices.Equal(got, want) || hasMark(t, pickedUp, ledger.MarkHistoryRemote) {
		t.Errorf("the story moved into work holds %v, want its whole chain %v", got, want)
	}
}
