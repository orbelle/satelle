//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_434d2fd9 AC1: a story whose transitions and gate rounds are older than the
// newest 10,000 rows still shows its stages.
func TestSlowSnapshotProgressSurvivesDeepLedger(t *testing.T) {
	e := newProgressEnv(t)
	old := e.story(t, "plan")
	e.append(t, edge(old, workitem.StatusBacklog, "plan")...)
	busy := e.stories(t, 100, workitem.StatusInProgress)
	e.noise(t, ledger.KindComment, exportWindow+50, busy...)
	e.outsideWindow(t, old)

	cell := e.progressCell(t, old)
	// The backlog→plan gates are backlog's exit gates, on its chip (sty_0b1abacc).
	for _, want := range []string{`class="stage-chip stage-current"`, `>backlog <b class="gates tone-`, `title="plan"`} {
		if !strings.Contains(cell, want) {
			t.Errorf("PROGRESS cell of %s missing %q: %q", old, want, cell)
		}
	}
}

// sty_434d2fd9 AC2: a rebuild-style burst of rows on other stories leaves an
// unrelated story's stages exactly as they were.
func TestSlowSnapshotProgressSurvivesRowBurst(t *testing.T) {
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
func TestSlowSnapshotLedgerExportBounded(t *testing.T) {
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
