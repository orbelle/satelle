package cli

// Paged work-state Snapshot (sty_2cb4e1f6): every verb that pulls work-state
// pages through the project, sends the last pass's token as since_token, resumes
// an interrupted pass from its checkpoint, and falls back to a full pass when
// the server refuses a token.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/store"
)

// pagingEnv is a bound repo against the fake server with a page size small
// enough that a handful of rows spans several pages. Flag-free commands resolve
// the server from the global config, as the serve reconcile loop's child does.
func pagingEnv(t *testing.T, pageSize int32) (url string, f *fakeWorkstateServer, repo string) {
	t.Helper()
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo = workstateRepo(t, mergeToml)
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}
	prev := hosted.DefaultSnapshotPageSize
	hosted.DefaultSnapshotPageSize = pageSize
	t.Cleanup(func() { hosted.DefaultSnapshotPageSize = prev })
	return ts.URL, f, repo
}

// seedChanged puts a story and a ledger row on the fake as changed now, so a
// since pass that follows an earlier token returns them.
func seedChanged(f *fakeWorkstateServer, suffix, title string) {
	at := time.Now().UTC()
	id, led := "sty_pg"+suffix, "led_pg"+suffix
	seedHostedStory(f, id, title, "backlog", at)
	seedHostedLedger(f, led, id, "note", map[string]any{"s": suffix}, at)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.itemVer = f.stamp(f.itemVer, "probe", id)
	f.ledgerVer = f.stamp(f.ledgerVer, "probe", led)
}

func seedChangedN(f *fakeWorkstateServer, n int) {
	for i := 1; i <= n; i++ {
		seedChanged(f, fmt.Sprintf("%02d", i), "Paged story")
	}
}

func localLedgerHas(t *testing.T, id string) bool {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, ok, err := db.Ledger.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// wantLocalRows asserts each story is listed exactly once and each ledger row
// is in the store.
func wantLocalRows(t *testing.T, n int) {
	t.Helper()
	list, err := runRoot(t, "story", "list", "--limit", "50")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, list)
	}
	for i := 1; i <= n; i++ {
		suffix := fmt.Sprintf("%02d", i)
		if c := strings.Count(list, "sty_pg"+suffix); c != 1 {
			t.Errorf("story sty_pg%s listed %d time(s), want 1:\n%s", suffix, c, list)
		}
		if !localLedgerHas(t, "led_pg"+suffix) {
			t.Errorf("ledger row led_pg%s missing from the local store", suffix)
		}
	}
}

func syncStateFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(hosted.DocumentSyncStatePathOverride)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pageTokensOf(reqs []*syncpb.SnapshotRequest) []string {
	var out []string
	for _, r := range reqs {
		out = append(out, r.GetPageToken())
	}
	return out
}
