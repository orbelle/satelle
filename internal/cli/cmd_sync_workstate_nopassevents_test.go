package cli

// No sync pass appends an event to a story's chain (sty_199ad82b): a pull,
// snapshot or rehydrate exchanges existing events only, and what it finds about a
// story is recorded in the work-state sync state. Chains are read read-only
// (passChain), so the seeding store.Open does is not mistaken for the pass's.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

const pageOnlyID = "sty_pageonly01"

// passFixture is a project published from another checkout — the stories taken,
// forked and edited, each with its hosted chain — plus, with pageOnly, a legacy
// story the server holds only as a page row and one page ledger row. The session
// is on a second, empty checkout bound to it.
type passFixture struct {
	ts                    *httptest.Server
	f                     *fakeWorkstateServer
	repo                  string
	taken, forked, edited string
	wants                 map[string][]ledger.Entry
	pageOnly              bool
}

func newPassFixture(t *testing.T, pageOnly bool) *passFixture {
	t.Helper()
	ts, f := newFakeWorkstateServerWith(t, func(mux *http.ServeMux, _ *fakeWorkstateServer) {
		mux.HandleFunc("GET /api/v1/projects/{project}/config", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]any{})
		})
		// A personal scope syncs documents too: rehydrate's documents leg finds none.
		mux.HandleFunc("GET /api/v1/projects/{project}/documents", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "cursor": ""})
		})
	})
	ids, wants, repo := publishedStoriesOn(t, ts, f, nil, "taken", "forked", "edited")
	if pageOnly {
		at := time.Now().Add(time.Hour).UTC()
		seedHostedFull(f, pageOnlyID, "Page only", "B", "1. x", "backlog", []string{"x"}, at)
		seedHostedLedger(f, "led_pageonly01", pageOnlyID, ledger.KindStoryCreated, map[string]any{"source": "legacy"}, at)
	}
	return &passFixture{ts: ts, f: f, repo: repo, taken: ids[0], forked: ids[1], edited: ids[2], wants: wants, pageOnly: pageOnly}
}

// fork gives this checkout the forked story's hosted chain up to its creation and
// then an event of its own, so the next hosted event builds on one the local chain
// already builds on differently.
func (fx *passFixture) fork(t *testing.T) {
	t.Helper()
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		for _, e := range fx.wants[fx.forked] {
			if _, err := db.Ledger.Upsert(ctx, e, time.Now()); err != nil {
				t.Fatal(err)
			}
			if e.Kind == ledger.KindStoryCreated {
				break
			}
		}
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: fx.forked, Kind: ledger.KindComment, Actor: "bob", Body: "local fork"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
}

// edit takes the edited story's hosted chain onto this checkout and retitles it
// here, so its chain folds to a definition the hosted row does not carry.
func (fx *passFixture) edit(t *testing.T) {
	t.Helper()
	if out, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL, fx.edited); err != nil {
		t.Fatalf("scoped pull: %v\n%s", err, out)
	}
	time.Sleep(10 * time.Millisecond) // time-subject: the local edit must be strictly newer than the hosted copy
	if out, err := runRoot(t, "story", "set", fx.edited, "--title", "Local edit"); err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
}

// chains reads every fixture story's chain as the last pass left it.
func (fx *passFixture) chains(t *testing.T) map[string][]ledger.Entry {
	t.Helper()
	ids := []string{fx.taken, fx.forked, fx.edited}
	if fx.pageOnly {
		ids = append(ids, pageOnlyID)
	}
	out := map[string][]ledger.Entry{}
	for _, id := range ids {
		out[id] = passChain(t, id)
	}
	return out
}

// known is every event id a pass may hold without having made it: the hosted
// chains and the local chains before the pass.
func (fx *passFixture) known(before map[string][]ledger.Entry) map[string]bool {
	seen := map[string]bool{"led_pageonly01": true}
	for _, chains := range []map[string][]ledger.Entry{fx.wants, before} {
		for _, chain := range chains {
			for _, e := range chain {
				seen[e.ID] = true
			}
		}
	}
	return seen
}

// noPassEvents fails for any event on a chain that neither the hosted chains nor
// the local chains before the pass held.
func noPassEvents(t *testing.T, what string, known map[string]bool, after map[string][]ledger.Entry) {
	t.Helper()
	for id, chain := range after {
		for _, e := range chain {
			if !known[e.ID] {
				t.Errorf("%s made %s (%s) on %s", what, e.ID, e.Kind, id)
			}
		}
	}
}

func chainOf(t *testing.T, chain []ledger.Entry) []ledger.Entry {
	t.Helper()
	order, err := chainOrder(chain)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

// sty_199ad82b AC2: with no chain service a rehydrate seeds nothing: a story the
// server holds is left without events rather than given a genesis of the pull's.
func TestRehydrateWithoutAChainServiceAppendsNothing(t *testing.T) {
	fx := newPassFixture(t, true)
	fx.f.chain.unreadable = true
	before := fx.chains(t)

	if out, err := runRoot(t, "sync", "rehydrate", "--server", fx.ts.URL, "--force"); err != nil {
		t.Fatalf("rehydrate: %v\n%s", err, out)
	}
	after := fx.chains(t)
	if got := after[fx.taken]; len(got) != 0 {
		t.Errorf("chain of %s = %v, want none: the server could not be read", fx.taken, idsOf(got))
	}
	noPassEvents(t, "the rehydrate", fx.known(before), after)
}

// sty_199ad82b AC1: a forced rehydrate of a story whose linear chain folds to a
// definition the hosted row does not carry leaves the chain as it was, puts the
// hosted copy on the row and records the disagreement.
func TestForcedRehydrateOfALocallyEditedStoryRecordsTheConflict(t *testing.T) {
	fx := newPassFixture(t, true)
	fx.edit(t)
	before := fx.chains(t)

	if out, err := runRoot(t, "sync", "rehydrate", "--server", fx.ts.URL, "--force"); err != nil {
		t.Fatalf("rehydrate --force: %v\n%s", err, out)
	}
	after := fx.chains(t)
	sameEvents(t, "the forced rehydrate", before[fx.edited], after[fx.edited])
	noPassEvents(t, "the forced rehydrate", fx.known(before), after)
	if row := passRow(t, fx.edited); row.Title != "edited" {
		t.Errorf("row title = %q, want the hosted copy's", row.Title)
	}
	if c := storyConflicts(t, fx.repo)[fx.edited]; c.Reason != conflictForcedHostedWins || c.LocalHolder == "" || c.RemoteHolder == "" {
		t.Errorf("conflict = %+v, want %q with both holders named", c, conflictForcedHostedWins)
	}
}

// sty_199ad82b AC3: after a pull that met a fork and a forced rehydrate that met a
// local edit, a push sends no overlap event and no workflow_version or
// definition_snapshot event a pass made: every one it carries was on a chain
// before the passes. The fixture holds no legacy page-only story: store.Open seeds
// one whose chain defines no title (sty_8de247b8 owns that), and the push would
// carry that seed, which no pass made.
func TestPushAfterConflictingPassesSendsNoPassEvent(t *testing.T) {
	fx := newPassFixture(t, false)
	fx.fork(t)
	fx.edit(t)
	before := fx.chains(t)
	known := fx.known(before)
	if out, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL); err == nil {
		t.Logf("pull: %s", out) // the edited story may refuse it; either way the pass ran
	}
	if out, err := runRoot(t, "sync", "rehydrate", "--server", fx.ts.URL, "--force"); err != nil {
		t.Fatalf("rehydrate --force: %v\n%s", err, out)
	}
	if c := storyConflicts(t, fx.repo); c[fx.forked].Reason != conflictChainForked || c[fx.edited].Reason != conflictForcedHostedWins {
		t.Fatalf("precondition: conflicts = %+v, want the fork and the forced win recorded", c)
	}
	sent := len(fx.f.chain.appends)

	_, _ = pushRaw(t, fx.ts.URL) // the forked story's push is refused; the payloads are the subject
	fx.f.chain.mu.Lock()
	appends := fx.f.chain.appends[sent:]
	fx.f.chain.mu.Unlock()
	for _, req := range appends {
		for _, e := range req.GetEvents() {
			var rec struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(e.GetPayload(), &rec); err != nil {
				t.Fatalf("event %s payload: %v", e.GetId(), err)
			}
			switch rec.Kind {
			case ledger.KindOverlap:
				t.Errorf("the push sent overlap event %s for %s", e.GetId(), req.GetStoryId())
			case ledger.KindWorkflowVersion, ledger.KindDefinitionSnapshot:
				if !known[e.GetId()] {
					t.Errorf("the push sent %s %s for %s, which a pass made", rec.Kind, e.GetId(), req.GetStoryId())
				}
			}
		}
	}
}
