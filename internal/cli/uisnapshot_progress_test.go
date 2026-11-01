package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/web"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// exportWindow is the full snapshot's recent-history budget (buildUISnapshotOpts).
const exportWindow = 10000

// progressEnv is a store whose ledger rows are stamped from its own clock, one
// millisecond apart, so which rows the export window holds is deterministic, and
// a mirror the store's full snapshots are pushed into, as the web service does.
type progressEnv struct {
	ctx   context.Context
	db    *store.DB
	a     *app.App
	clock time.Time
	base  time.Time
	seq   int
	mux   *http.ServeMux
	srv   *httptest.Server
}

func newProgressEnv(t *testing.T) *progressEnv {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	_ = os.WriteFile(filepath.Join(repo, ".git"), []byte(""), 0o644)
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: ms}).Mount(mux)
	srv := httptest.NewServer(web.NewMirror(ms).Handler)
	t.Cleanup(srv.Close)
	// Ahead of the wall clock: a row the store stamps itself (a workflow_version)
	// is older than every fixture row, so it never takes a window slot.
	base := time.Now().UTC().Add(time.Hour)
	return &progressEnv{
		ctx: context.Background(), db: db, mux: mux, srv: srv, clock: base, base: base,
		a: &app.App{Config: config.Config{}, RepoRoot: repo, Store: db},
	}
}

func (e *progressEnv) story(t *testing.T, status string) string {
	t.Helper()
	sty, err := e.db.Stories.Create(e.ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "Progress " + status, Body: "goal",
		AcceptanceCriteria: "1. x", Category: "chore", Status: status,
	}, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	return sty.ID
}

// stories creates n stories at status.
func (e *progressEnv) stories(t *testing.T, n int, status string) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = e.story(t, status)
	}
	return ids
}

// progress is one row per kind on the from→to edge, each verdict its own round
// of one gate.
func progress(storyID, from, to string, kinds ...string) []ledger.AppendInput {
	out := make([]ledger.AppendInput, 0, len(kinds))
	for _, kind := range kinds {
		p, _ := json.Marshal(map[string]string{"from": from, "to": to, "attempt": ledger.NewAttemptID(), "skill": "rev"})
		out = append(out, ledger.AppendInput{StoryID: storyID, Kind: kind, Payload: p})
	}
	return out
}

// edge is an accepted gate round on from→to and the transition it took.
func edge(storyID, from, to string) []ledger.AppendInput {
	return progress(storyID, from, to, ledger.KindReviewAccept, ledger.KindStatusTransition)
}

// noise writes n rows of kind, newer than every row so far, dealt across
// storyIDs in turn. Their ids are sequential: Append's random 32-bit ids collide
// often enough over ten thousand rows to make the fixture flaky.
func (e *progressEnv) noise(t *testing.T, kind string, n int, storyIDs ...string) {
	t.Helper()
	err := e.db.InTx(e.ctx, func(tx *sql.Tx) error {
		led := e.db.Ledger.WithTx(tx)
		for i := range n {
			e.seq++
			e.clock = e.clock.Add(time.Millisecond)
			if _, err := led.Upsert(e.ctx, ledger.Entry{
				ID: fmt.Sprintf("evt_noise%07d", e.seq), StoryID: storyIDs[i%len(storyIDs)],
				Kind: kind, Body: "noise", CreatedAt: e.clock,
			}, e.clock); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// append writes rows newer than every row so far, a millisecond apart.
func (e *progressEnv) append(t *testing.T, rows ...ledger.AppendInput) []ledger.Entry {
	t.Helper()
	out := e.appendFrom(t, e.clock, rows...)
	e.clock = e.clock.Add(time.Duration(len(rows)) * time.Millisecond)
	return out
}

// appendFrom writes rows in one transaction, from at onward, a millisecond apart.
func (e *progressEnv) appendFrom(t *testing.T, at time.Time, rows ...ledger.AppendInput) []ledger.Entry {
	t.Helper()
	out := make([]ledger.Entry, 0, len(rows))
	err := e.db.InTx(e.ctx, func(tx *sql.Tx) error {
		led := e.db.Ledger.WithTx(tx)
		for i, in := range rows {
			ent, err := led.Append(e.ctx, in, at.Add(time.Duration(i+1)*time.Millisecond))
			if err != nil {
				return err
			}
			out = append(out, ent)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// outsideWindow fails the test unless every row of storyID is older than the
// newest exportWindow rows — the rows a window-only export dropped.
func (e *progressEnv) outsideWindow(t *testing.T, storyID string) {
	t.Helper()
	window, err := e.db.Ledger.ListAll(e.ctx, exportWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range window {
		if r.StoryID == storyID {
			t.Fatalf("fixture: %s row %s is inside the newest %d rows", storyID, r.ID, exportWindow)
		}
	}
}

func (e *progressEnv) snapshot(t *testing.T) *mirror.Snapshot {
	t.Helper()
	snap, err := buildUISnapshot(e.ctx, e.a)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// progressCell pushes a full snapshot into the mirror and returns the PROGRESS
// cell of storyID's row on the story list page.
func (e *progressEnv) progressCell(t *testing.T, storyID string) string {
	t.Helper()
	body, err := json.Marshal(e.snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/snapshot", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body)
	}
	resp, err := http.Get(e.srv.URL + "/r/" + partitionSlug(e.a) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	_, row, ok := strings.Cut(string(page), `data-id="`+storyID+`"`)
	if !ok {
		t.Fatalf("page has no row for %s", storyID)
	}
	_, cell, ok := strings.Cut(row, `class="col-reviews">`)
	if !ok {
		t.Fatalf("row of %s has no PROGRESS cell", storyID)
	}
	cell, _, _ = strings.Cut(cell, "</td>")
	return cell
}

// sty_434d2fd9 AC1: a story whose transitions and gate rounds are older than the
// newest 10,000 rows still shows its stages.
func TestSnapshotProgressSurvivesDeepLedger(t *testing.T) {
	e := newProgressEnv(t)
	old := e.story(t, "plan")
	e.append(t, edge(old, workitem.StatusBacklog, "plan")...)
	busy := e.stories(t, 100, workitem.StatusInProgress)
	e.noise(t, ledger.KindComment, exportWindow+50, busy...)
	e.outsideWindow(t, old)

	cell := e.progressCell(t, old)
	// The chip counts the plan edge's gates, every one passed (sty_216d5205).
	for _, want := range []string{`class="stage-chip stage-current"`, `>plan <b class="gates">`, `<b class="rej">0</b>`} {
		if !strings.Contains(cell, want) {
			t.Errorf("PROGRESS cell of %s missing %q: %q", old, want, cell)
		}
	}
}

// sty_434d2fd9 AC2: a rebuild-style burst of rows on other stories leaves an
// unrelated story's stages exactly as they were.
func TestSnapshotProgressSurvivesRowBurst(t *testing.T) {
	e := newProgressEnv(t)
	sty := e.story(t, workitem.StatusInProgress)
	e.append(t, edge(sty, workitem.StatusBacklog, "plan")...)
	e.append(t, edge(sty, "plan", workitem.StatusInProgress)...)
	before := e.progressCell(t, sty)
	if !strings.Contains(before, "in_progress") || !strings.Contains(before, "plan") {
		t.Fatalf("PROGRESS cell before the burst = %q, want the plan and in_progress stages", before)
	}

	e.noise(t, ledger.KindStatusReconcile, 100*101, e.stories(t, 100, workitem.StatusBacklog)...)
	e.outsideWindow(t, sty)

	if after := e.progressCell(t, sty); after != before {
		t.Errorf("PROGRESS cell after the burst = %q, want it unchanged: %q", after, before)
	}
}

// sty_434d2fd9 AC3: the full snapshot's ledger is the recent window plus the
// progress rows outside it — it grows with progress events, not with the ledger.
func TestSnapshotLedgerExportBounded(t *testing.T) {
	e := newProgressEnv(t)
	ids := e.stories(t, 20, "plan")
	for _, id := range ids {
		e.append(t, progress(id, workitem.StatusBacklog, "plan",
			ledger.KindReviewReject, ledger.KindReviewAccept, ledger.KindStatusTransition)...)
	}
	stale := e.append(t, ledger.AppendInput{StoryID: ids[0], Kind: ledger.KindComment, Body: "old"})[0]
	busy := e.stories(t, 100, workitem.StatusInProgress)
	e.noise(t, ledger.KindComment, exportWindow+50, busy...)

	count := func(want int) []json.RawMessage {
		t.Helper()
		got := e.snapshot(t).LedgerEvents
		if len(got) != want {
			t.Fatalf("exported ledger rows = %d, want %d", len(got), want)
		}
		return got
	}
	for _, raw := range count(exportWindow + 60) {
		var r ledger.Entry
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.ID == stale.ID {
			t.Errorf("a %s row outside the window was exported: %s", r.Kind, r.ID)
		}
	}

	e.noise(t, ledger.KindComment, 5000, busy...)
	count(exportWindow + 60)

	e.appendFrom(t, e.base.Add(-time.Minute), progress(ids[0], "plan", workitem.StatusInProgress,
		ledger.KindReviewReject, ledger.KindReviewReject, ledger.KindPerformerReject,
		ledger.KindReviewAccept, ledger.KindReadinessAccept)...)
	count(exportWindow + 65)
}
