package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// forkedFromHosted is a story this checkout pulled whole, after which another
// machine appended an event to its hosted chain: local work built on the pulled
// tip then forks from the hosted chain, which builds on that tip differently.
// want is the chain both sides held before the other machine's event.
func forkedFromHosted(t *testing.T) (ts *httptest.Server, f *fakeWorkstateServer, id string, want []ledger.Entry, repo string) {
	t.Helper()
	ts, f, id, want, repo = publishedElsewhere(t)
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	otherMachineEvent(t, f, id, "made elsewhere", time.Now().UTC())
	if got := storyStatus(t, id); got != "backlog" {
		t.Fatalf("fixture status = %q, want backlog", got)
	}
	return ts, f, id, want, repo
}

// localEvent is local work on story, built on base, made at.
func localEvent(story, id, base, kind, body string, at time.Time) ledger.Entry {
	return ledger.Entry{ID: id, StoryID: story, BaseID: base, Kind: kind, Actor: "bob", Body: body, CreatedAt: at}
}

func localMove(story, id, base, from, to string, at time.Time) ledger.Entry {
	e := localEvent(story, id, base, ledger.KindStatusTransition, from+" → "+to, at)
	e.Payload = json.RawMessage(fmt.Sprintf(`{"from":%q,"to":%q}`, from, to))
	return e
}

// mergedChain is the story's local chain in chain order and the record its last
// event carries, failing unless that event is a chain_merge.
func mergedChain(t *testing.T, id string) ([]ledger.Entry, chainMergeRecord) {
	t.Helper()
	order, err := chainOrder(localChain(t, id))
	if err != nil {
		t.Fatalf("the chain after rechain is not one chain: %v", err)
	}
	last := order[len(order)-1]
	if last.Kind != ledger.KindChainMerge {
		t.Fatalf("chain after rechain = %v, want it to end with a chain_merge", chainShape(order))
	}
	var rec chainMergeRecord
	if err := json.Unmarshal(last.Payload, &rec); err != nil {
		t.Fatalf("chain_merge payload %s: %v", last.Payload, err)
	}
	return order, rec
}

// fullChain renders every stored field of a story's local events, read as stored
// and in no chain order, so a refusal can be shown to leave them exactly as they
// were.
func fullChain(t *testing.T, id string) []string {
	t.Helper()
	var events []ledger.Entry
	withRepoStore(t, func(db *store.DB) {
		var err error
		if events, err = db.Ledger.StoryChain(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	})
	var out []string
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s<-%s %s %q %s %s %s %s", e.ID, e.BaseID, e.Kind, e.Body, e.Payload, e.Refs, e.CreatedAt.Format(time.RFC3339Nano), e.WorkflowHash))
	}
	return out
}

// sty_f321c29e AC1: two local branches off the pulled tip, whose times
// interleave, are merged after the hosted chain in event time, none before its
// base, then a chain_merge names the fork and the merged events, and the status is
// the fold of the merged chain.
func TestRechainMergesForkedChain(t *testing.T) {
	ts, f, id, want, repo := forkedFromHosted(t)
	hostedIDs := f.chain.stored("probe", id)
	tip, t0 := want[len(want)-1].ID, time.Now().UTC()
	setLocalChain(t, id, spliced(want, []ledger.Entry{
		localEvent(id, "evt_l1", tip, ledger.KindComment, "first", t0.Add(1*time.Second)),
		localEvent(id, "evt_l3", "evt_l1", ledger.KindComment, "third", t0.Add(3*time.Second)),
		localMove(id, "evt_l2", tip, "backlog", "plan", t0.Add(2*time.Second)),
	}))

	out, err := runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL)
	if err != nil || !strings.Contains(out, "rechained "+id) || !strings.Contains(out, "merge 3 local event(s), exclude 0") {
		t.Fatalf("rechain = %v\n%s, want the three local events merged", err, out)
	}
	order, rec := mergedChain(t, id)
	wantIDs := append(slices.Clone(hostedIDs), "evt_l1", "evt_l2", "evt_l3")
	if got := idsOf(order[:len(order)-1]); !slices.Equal(got, wantIDs) {
		t.Fatalf("chain after rechain = %v, want the hosted chain then the local events in event time %v", got, wantIDs)
	}
	for i := len(hostedIDs); i < len(order); i++ {
		if order[i].BaseID != order[i-1].ID {
			t.Errorf("%s builds on %s, want %s", order[i].ID, order[i].BaseID, order[i-1].ID)
		}
	}
	if rec.ForkBase != tip || rec.HostedLatest != hostedIDs[len(hostedIDs)-1] || !slices.Equal(rec.Merged, []string{"evt_l1", "evt_l2", "evt_l3"}) || len(rec.Excluded) != 0 || len(rec.Dropped) != 0 {
		t.Errorf("chain_merge record = %+v, want fork %s, hosted latest %s and the three merged", rec, tip, hostedIDs[len(hostedIDs)-1])
	}
	if got := storyStatus(t, id); got != "plan" {
		t.Errorf("status after the merge = %q, want plan, the fold of the merged chain", got)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after rechain = %v, want none", c)
	}
}

// sty_f321c29e AC1: inside a fork a workflow_version is work, merged at its time,
// and only the seeded definition snapshot is dropped, named in the record; every
// local event is in the chain or the record.
func TestRechainMergeKeepsWorkflowVersionInFork(t *testing.T) {
	ts, f, id, want, _ := forkedFromHosted(t)
	hostedIDs := f.chain.stored("probe", id)
	tip, t0 := want[len(want)-1].ID, time.Now().UTC()
	version := localEvent(id, "evt_v", "evt_l1", ledger.KindWorkflowVersion, "workflow version h", t0.Add(2*time.Second))
	version.Actor, version.Refs = "satelle", json.RawMessage(`{"workflow_version":"`+id+`:h"}`)
	snapshot := localEvent(id, "evt_s", "evt_v", ledger.KindDefinitionSnapshot, "definition snapshot seeded from the story row", t0.Add(3*time.Second))
	snapshot.Actor, snapshot.Refs = "satelle", json.RawMessage(`{"definition_snapshot":"`+id+`"}`)
	if !ledger.IsVersionEvent(version) || !isSeededSnapshot(snapshot) {
		t.Fatal("fixture: the version and the snapshot are not the shapes the merge judges")
	}
	local := []ledger.Entry{
		localEvent(id, "evt_l1", tip, ledger.KindComment, "work", t0.Add(1*time.Second)),
		version, snapshot,
		localEvent(id, "evt_l4", "evt_s", ledger.KindComment, "more work", t0.Add(4*time.Second)),
	}
	setLocalChain(t, id, spliced(want, local))

	if out, err := runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL); err != nil || !strings.Contains(out, "rechained "+id) {
		t.Fatalf("rechain = %v\n%s", err, out)
	}
	order, rec := mergedChain(t, id)
	wantIDs := append(slices.Clone(hostedIDs), "evt_l1", "evt_v", "evt_l4")
	if got := idsOf(order[:len(order)-1]); !slices.Equal(got, wantIDs) {
		t.Fatalf("chain after rechain = %v, want %v: the workflow_version kept at its time, the snapshot dropped", got, wantIDs)
	}
	if !slices.Equal(rec.Merged, []string{"evt_l1", "evt_v", "evt_l4"}) || !slices.Equal(rec.Dropped, []string{"evt_s"}) {
		t.Errorf("chain_merge record = %+v, want the work merged and the snapshot dropped", rec)
	}
	held := map[string]bool{}
	for _, e := range order {
		held[e.ID] = true
	}
	for _, id := range append(rec.Merged, rec.Dropped...) {
		held[id] = true
	}
	for _, e := range local {
		if !held[e.ID] {
			t.Errorf("%s is in neither the chain nor the record", e.ID)
		}
	}
	if got := storyStatus(t, id); got != "backlog" {
		t.Errorf("status after the merge = %q, want backlog, the fold of the hosted chain and the work", got)
	}
}

// sty_f321c29e AC2: a local move invalid after the status the hosted chain folds
// to is named with from → to, left out of the chain and listed in the record, and
// the status does not move.
func TestRechainExcludesInvalidMergedTransition(t *testing.T) {
	ts, f, id, want, _ := forkedFromHosted(t)
	otherMachineTransition(t, f, id, "backlog", "plan")
	otherMachineTransition(t, f, id, "plan", "in_progress")
	tip, t0 := want[len(want)-1].ID, time.Now().UTC()
	setLocalChain(t, id, spliced(want, []ledger.Entry{
		localEvent(id, "evt_l1", tip, ledger.KindComment, "work", t0.Add(time.Second)),
		localMove(id, "evt_l2", "evt_l1", "backlog", "plan", t0.Add(2*time.Second)),
	}))

	out, err := runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL)
	if err != nil || !strings.Contains(out, "excluded evt_l2 (backlog → plan): invalid after in_progress") {
		t.Fatalf("rechain = %v\n%s, want the move named as excluded", err, out)
	}
	order, rec := mergedChain(t, id)
	if slices.Contains(idsOf(order), "evt_l2") {
		t.Errorf("chain after rechain = %v, want the excluded move left out", idsOf(order))
	}
	if len(rec.Excluded) != 1 || rec.Excluded[0].ID != "evt_l2" || rec.Excluded[0].From != "backlog" || rec.Excluded[0].To != "plan" || !slices.Equal(rec.Merged, []string{"evt_l1"}) {
		t.Errorf("chain_merge record = %+v, want evt_l1 merged and evt_l2 excluded", rec)
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Errorf("status after the merge = %q, want in_progress", got)
	}
}

// sty_f321c29e AC4: a fork with a local event built on an event neither side
// holds is refused by name with the reason, its chain exactly as it was, dry run
// or not.
func TestRechainRefusesUnmergeableFork(t *testing.T) {
	ts, _, id, want, _ := forkedFromHosted(t)
	tip, t0 := want[len(want)-1].ID, time.Now().UTC()
	setLocalChain(t, id, spliced(want, []ledger.Entry{
		localEvent(id, "evt_l1", tip, ledger.KindComment, "work", t0.Add(time.Second)),
		localEvent(id, "evt_l9", "evt_absent", ledger.KindComment, "orphan", t0.Add(2*time.Second)),
	}))
	before := fullChain(t, id)

	for _, args := range [][]string{{"--dry-run"}, {}} {
		out, err := runRoot(t, append([]string{"sync", "workstate", "rechain", id, "--server", ts.URL}, args...)...)
		if err == nil || !strings.HasPrefix(rechainOutcomes(t, out)[id], "refuse: evt_l9 builds on evt_absent, which neither side holds") {
			t.Fatalf("rechain %v = %v\n%s, want the story refused naming the dangling base", args, err, out)
		}
		if got := fullChain(t, id); !slices.Equal(got, before) {
			t.Errorf("rechain %v changed the chain:\n%v\nwant\n%v", args, got, before)
		}
	}
}

// sty_f321c29e AC3, AC6, AC7: a push leaves a forked story unpublished, records it
// in the story-conflict set and names rechain, dry run first and no --force, as
// the fix; rechain --conflicted then takes that story and not another that only
// differs from its hosted chain.
func TestPushRecordsForkedStoryAsConflict(t *testing.T) {
	ts, f, ids, wants, repo := publishedStories(t, "forked", "reordered")
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	forked, reordered := ids[0], ids[1]
	otherMachineEvent(t, f, forked, "made elsewhere", time.Now().UTC())
	tip, t0 := wants[forked][len(wants[forked])-1].ID, time.Now().UTC()
	setLocalChain(t, forked, spliced(wants[forked], []ledger.Entry{
		localEvent(forked, "evt_l1", tip, ledger.KindComment, "one", t0.Add(time.Second)),
		localEvent(forked, "evt_l2", tip, ledger.KindComment, "two", t0.Add(2*time.Second)),
	}))
	w := wants[reordered]
	setLocalChain(t, reordered, relinked(spliced([]ledger.Entry{w[1], w[0]}, w[2:])...))

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("push of a forked story exited 0:\n%s", out)
	}
	if c, ok := storyConflicts(t, repo)[forked]; !ok || !strings.Contains(c.Reason, "forks at "+tip) {
		t.Errorf("conflict of %s = %+v (recorded %v), want the fork recorded", forked, c, ok)
	}
	if _, ok := storyConflicts(t, repo)[reordered]; ok {
		t.Fatalf("fixture: %s is recorded in conflict, want it only divergent", reordered)
	}
	for _, text := range []string{err.Error(), health.RechainRemediation} {
		if !strings.Contains(text, "`satelle sync workstate rechain --dry-run`, then `satelle sync workstate rechain`") || strings.Contains(text, "--force") {
			t.Errorf("push fix = %q, want rechain dry run first and no --force", text)
		}
	}

	out, err = runRoot(t, "sync", "workstate", "rechain", "--conflicted", "--dry-run", "--server", ts.URL)
	got := rechainOutcomes(t, out)
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[forked], "rechain: ") {
		t.Errorf("rechain --conflicted = %v, reported %v, want only %s\n%s", err, got, forked, out)
	}
	out, _ = runRoot(t, "sync", "workstate", "rechain", "--dry-run", "--server", ts.URL)
	if got := rechainOutcomes(t, out); len(got) != 2 {
		t.Errorf("rechain without --conflicted reported %v, want both stories", got)
	}
}

// sty_f321c29e AC6: the drift finding names rechain, dry run first, before the
// row repair, and never --force.
func TestStatusDriftFindingNamesRechain(t *testing.T) {
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "drifted")
	db := openScopeDB(t)
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: id, Kind: ledger.KindStatusTransition, Body: "backlog → plan",
		Payload: mustJSON(transitionPayload("backlog", "plan")),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var fix string
	for _, f := range statusDriftFindings(repo) {
		if f.ID == health.IDStatusDrift && f.Artifact == id {
			fix = f.Remediation
		}
	}
	if !strings.Contains(fix, "`satelle sync workstate rechain --dry-run`, then `satelle sync workstate rechain`") || strings.Contains(fix, "--force") {
		t.Errorf("drift fix = %q, want rechain dry run first and no --force", fix)
	}
	if i, j := strings.Index(fix, "rechain --dry-run"), strings.Index(fix, "reconcile --repair"); i < 0 || j < i {
		t.Errorf("drift fix = %q, want rechain named before the row repair", fix)
	}
}
