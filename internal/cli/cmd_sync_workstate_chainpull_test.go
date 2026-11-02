package cli

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// publishedElsewhere has one checkout create a story with a multi-event chain and push
// it, so the project holds the story's chain. The server then answers as the real
// one does — its work-state page carries no story events — and the session moves
// to a second, empty checkout.
func publishedElsewhere(t *testing.T) (ts *httptest.Server, f *fakeWorkstateServer, id string, want []ledger.Entry, repo string) {
	t.Helper()
	ts, f, ids, wants, repo := publishedStories(t, "pulled")
	return ts, f, ids[0], wants[ids[0]], repo
}

// publishedStories is publishedElsewhere for several stories, one per title.
func publishedStories(t *testing.T, titles ...string) (ts *httptest.Server, f *fakeWorkstateServer, ids []string, wants map[string][]ledger.Entry, repo string) {
	t.Helper()
	return publishedStoriesWith(t, nil, titles...)
}

// publishedStoriesWith is publishedStories with extra events appended to each
// story's chain, after its comments, before it is published.
func publishedStoriesWith(t *testing.T, extra func(id string) []ledger.AppendInput, titles ...string) (ts *httptest.Server, f *fakeWorkstateServer, ids []string, wants map[string][]ledger.Entry, repo string) {
	t.Helper()
	ts, f = newFakeWorkstateServer(t)
	ids, wants, repo = publishedStoriesOn(t, ts, f, extra, titles...)
	return ts, f, ids, wants, repo
}

// publishedStoriesOn is publishedStoriesWith against a server the caller built.
func publishedStoriesOn(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer, extra func(id string) []ledger.AppendInput, titles ...string) (ids []string, wants map[string][]ledger.Entry, repo string) {
	t.Helper()
	seedCred(t, ts.URL)
	workstateRepo(t, holdOffToml)
	for _, title := range titles {
		ids = append(ids, createStoryID(t, title))
	}
	withRepoStore(t, func(db *store.DB) {
		for _, id := range ids {
			for _, body := range []string{"one", "two", "three"} {
				if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: id, Kind: ledger.KindComment, Actor: "bob", Body: body}, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if extra == nil {
				continue
			}
			for _, in := range extra(id) {
				if _, err := db.Ledger.Append(context.Background(), in, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			row, err := db.Stories.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verb.RebuildRow(context.Background(), db.Stories, db.Ledger, row, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	})
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	wants = map[string][]ledger.Entry{}
	for _, id := range ids {
		wants[id] = localChain(t, id)
		if len(wants[id]) < 4 {
			t.Fatalf("published chain of %s has %d events, want several", id, len(wants[id]))
		}
	}
	f.mu.Lock()
	f.ledgerByID = map[string]map[string]any{}
	f.mu.Unlock()
	repo = workstateRepo(t, holdOffToml)
	return ids, wants, repo
}

func pullRaw(t *testing.T, server string) (string, error) {
	t.Helper()
	return runRoot(t, "sync", "workstate", "pull", "--server", server)
}

func idsOf(chain []ledger.Entry) []string {
	var ids []string
	for _, e := range chain {
		ids = append(ids, e.ID)
	}
	return ids
}

func sameChain(t *testing.T, got, want []ledger.Entry) {
	t.Helper()
	if !slices.Equal(idsOf(got), idsOf(want)) {
		t.Fatalf("local chain = %v, want the hosted chain %v", idsOf(got), idsOf(want))
	}
	for i := range want {
		if got[i].BaseID != want[i].BaseID || got[i].Kind != want[i].Kind || got[i].Body != want[i].Body || got[i].WorkflowHash != want[i].WorkflowHash {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// sty_9b1dcf4b AC1, AC2: a pull of a story that exists only on the hosted project
// takes its hosted chain event for event, in chain order, and makes none of its own.
func TestPullMaterialisesHostedChain(t *testing.T) {
	ts, _, id, want, repo := publishedElsewhere(t)

	out, err := pullRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	got := localChain(t, id)
	sameChain(t, got, want)
	for _, e := range got {
		if strings.Contains(e.Body, "seeded from the story row") {
			t.Errorf("the pull made %s (%s) itself", e.ID, e.Kind)
		}
	}
	if head := storyHeads(t, ts.URL, "probe", repo)[id]; head != want[len(want)-1].ID {
		t.Errorf("head after the pull = %q, want the hosted latest %q", head, want[len(want)-1].ID)
	}
}

// sty_9b1dcf4b AC3: a push straight after such a pull appends nothing for the story
// and succeeds.
func TestPushAfterPullAppendsNothing(t *testing.T) {
	ts, f, id, _, repo := publishedElsewhere(t)
	before := len(f.chain.appendsFor(id))

	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push after pull: %v\n%s", err, out)
	}
	if n := len(f.chain.appendsFor(id)); n != before {
		t.Errorf("the push sent %d Append(s) for the pulled story, want none", n-before)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after pull and push = %v, want none", c)
	}
}

// forkedByOldPull reproduces a story an earlier pull forked: the server could not
// be read, so the pull seeded a genesis of its own, and the push of it was refused.
func forkedByOldPull(t *testing.T) (ts *httptest.Server, f *fakeWorkstateServer, id string, want []ledger.Entry, repo string) {
	t.Helper()
	ts, f, id, want, repo = publishedElsewhere(t)
	f.chain.unreadable = true
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	f.chain.unreadable = false
	seeded := localChain(t, id)
	if len(seeded) == 0 || slices.Contains(idsOf(want), seeded[0].ID) {
		t.Fatalf("local chain = %v, want a seeded genesis the hosted chain lacks", idsOf(seeded))
	}
	out, err := pushRaw(t, ts.URL)
	if err == nil || !strings.Contains(out, "moved since its start") {
		t.Fatalf("push of the forked story = %v\n%s, want it refused as moved since its start", err, out)
	}
	if _, ok := storyConflicts(t, repo)[id]; !ok {
		t.Fatalf("the refused push recorded no conflict for %s", id)
	}
	return ts, f, id, want, repo
}

// sty_9b1dcf4b AC4: rechain replaces exactly the seeded events of a forked story with
// its hosted chain, reports the story, clears its conflict, and the next push sends
// nothing.
func TestRechainRepairsSeededFork(t *testing.T) {
	ts, f, id, want, repo := forkedByOldPull(t)

	out, err := runRoot(t, "sync", "workstate", "rechain", "--dry-run", "--server", ts.URL)
	if err != nil || !strings.Contains(out, "would rechain "+id) {
		t.Fatalf("rechain --dry-run = %v\n%s", err, out)
	}
	if got := localChain(t, id); slices.Equal(idsOf(got), idsOf(want)) {
		t.Fatal("--dry-run changed the chain")
	}

	out, err = runRoot(t, "sync", "workstate", "rechain", "--server", ts.URL)
	if err != nil || !strings.Contains(out, "rechained "+id) {
		t.Fatalf("rechain = %v\n%s", err, out)
	}
	sameChain(t, localChain(t, id), want)
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after rechain = %v, want none", c)
	}

	before := len(f.chain.appendsFor(id))
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push after rechain: %v\n%s", err, out)
	}
	if n := len(f.chain.appendsFor(id)); n != before {
		t.Errorf("the push sent %d Append(s) after rechain, want none", n-before)
	}
}

// sty_9b1dcf4b AC4: a story with local work beyond a seeded genesis is refused by
// name and left untouched.
func TestRechainRefusesLocalWork(t *testing.T) {
	ts, _, id, want, _ := forkedByOldPull(t)
	withRepoStore(t, func(db *store.DB) {
		if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: id, Kind: ledger.KindComment, Actor: "bob", Body: "local work"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	before := idsOf(localChain(t, id))

	out, err := runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL)
	if err == nil || !strings.Contains(out, "refused "+id) || !strings.Contains(out, "not only a seeded genesis") {
		t.Fatalf("rechain = %v\n%s, want the story refused by name", err, out)
	}
	if got := idsOf(localChain(t, id)); !slices.Equal(got, before) {
		t.Errorf("a refused rechain changed the chain: %v, want %v", got, before)
	}
	if slices.Contains(before, want[0].ID) {
		t.Error("the refused story took hosted events")
	}
}

// seedLocalPrefix gives this checkout the events of prefix, under their hosted ids
// and bases, and saves the last of them as the story's head.
func seedLocalPrefix(t *testing.T, server, repo, id string, prefix []ledger.Entry) {
	t.Helper()
	withRepoStore(t, func(db *store.DB) {
		for _, e := range prefix {
			if _, err := db.Ledger.Upsert(context.Background(), e, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err := hosted.SaveStoryHead(server, "probe", repo, id, prefix[len(prefix)-1].ID); err != nil {
		t.Fatal(err)
	}
}

// sty_aa605c84 AC1: a story whose local event ids are a strict prefix of its hosted
// chain is fast-forwarded: the extra hosted events are stored under their hosted
// ids, no conflict is recorded and the head is the hosted latest.
func TestPullFastForwardsAStrictPrefix(t *testing.T) {
	ts, _, id, want, repo := publishedElsewhere(t)
	k := len(want) - 2
	seedLocalPrefix(t, ts.URL, repo, id, want[:k])

	out, err := pullRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	sameChain(t, localChain(t, id), want)
	if c, ok := storyConflicts(t, repo)[id]; ok {
		t.Errorf("the fast-forward recorded a conflict: %+v", c)
	}
	if strings.Contains(out, "rechain") {
		t.Errorf("the fast-forward was named for rechain:\n%s", out)
	}
	if head := storyHeads(t, ts.URL, "probe", repo)[id]; head != want[len(want)-1].ID {
		t.Errorf("head = %q, want the hosted latest %q", head, want[len(want)-1].ID)
	}
}

// sty_aa605c84 AC2: a story whose local chain has an unpublished event and whose
// hosted chain has a different event on the same base is recorded as forked, and
// no hosted event is taken.
func TestPullRecordsATrueFork(t *testing.T) {
	ts, _, id, want, repo := publishedElsewhere(t)
	k := 2
	seedLocalPrefix(t, ts.URL, repo, id, want[:k])
	appendLocal(t, id, "unpublished here")
	before := idsOf(localChain(t, id))
	if len(before) != k+1 || before[k] == want[k].ID {
		t.Fatalf("local chain = %v, want %v and a local event of its own", before, idsOf(want[:k]))
	}

	out, err := pullRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if c := storyConflicts(t, repo)[id]; c.Reason != conflictChainForked {
		t.Errorf("conflict = %+v, want %q", c, conflictChainForked)
	}
	if got := idsOf(localChain(t, id)); !slices.Equal(got, before) {
		t.Errorf("local chain = %v, want it untouched %v: no hosted event taken", got, before)
	}
	if head := storyHeads(t, ts.URL, "probe", repo)[id]; head != want[k-1].ID {
		t.Errorf("head = %q, want it unchanged at %q", head, want[k-1].ID)
	}
}
