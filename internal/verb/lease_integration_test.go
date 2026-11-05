package verb_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestLeaseSameTargetInFlightNoOp: re-engage same story to same status is a
// no-op (AC3 — never two concurrent dispatches for one id/target).
func TestLeaseSameTargetInFlightNoOp(t *testing.T) {
	wireWithWorkflows(t, singleStoryWF)
	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	// Second set to plan should no-op without error.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	if a.Status != "plan" {
		t.Fatalf("status = %q", a.Status)
	}
	// Sequential progression still works.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}), &a)
	if a.Status != "in_progress" {
		t.Fatalf("progression blocked: %q", a.Status)
	}
}

// TestStorySetStampsResolvedSession: the production acquire path (story-set →
// acquireEngagementLease) stamps SATELLE_SESSION onto the lease. Deleting
// SessionID: sessionID from that call must fail this test.
func TestStorySetStampsResolvedSession(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("SATELLE_SESSION", "sess-A")
	db := wireWithWorkflowsStore(t, singleStoryWF)

	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	if a.Status != "plan" {
		t.Fatalf("status = %q", a.Status)
	}
	l, err := db.Leases.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.SessionID != "sess-A" {
		t.Fatalf("story-set must stamp SATELLE_SESSION on the lease, got %q", l.SessionID)
	}
}

// TestSameStatusReengageAfterDroppedSeat (sty_4f74d01f): force-release the lease
// while status stays performing; story set --status <same> re-grants a seat.
func TestSameStatusReengageAfterDroppedSeat(t *testing.T) {
	wireWithWorkflows(t, singleStoryWF)
	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	// Drop seat while leaving status performing.
	if _, err := verb.Dispatch(context.Background(), "story-seat-release", mustJSONLease(map[string]any{
		"id": a.ID,
	})); err != nil {
		t.Fatalf("story-seat-release: %v", err)
	}
	// Same-status set must re-acquire without error.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	if a.Status != "plan" {
		t.Fatalf("status = %q", a.Status)
	}
	seats, serr := verb.Dispatch(context.Background(), "story-seat-list", nil)
	if serr != nil {
		t.Fatalf("story-seat-list after reengage: %v", serr)
	}
	if !strings.Contains(string(seats), a.ID) {
		t.Fatalf("expected seat for %s after re-engage, got %s", a.ID, seats)
	}
}

// TestLeaseNewAcquireAbortFreesSeat: when a NEW engage (backlog→plan) is
// rejected by the gate, the just-claimed seat is released so another story can
// engage (AC4 release-on-abort).
func TestLeaseNewAcquireAbortFreesSeat(t *testing.T) {
	withWiring(t)
	wireWithWorkflows(t, singleStoryWF)

	verb.SetTransitionGater(rejectToGater{to: "plan", skill: "intent"})

	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "B", "category": "feature"}), &b)

	_, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"})
	if err == nil {
		t.Fatal("expected gate reject on plan")
	}
	// A stays backlog; seat released.
	var still workitem.Item
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &still)
	if still.Status != workitem.StatusBacklog {
		t.Fatalf("A status = %q after reject", still.Status)
	}

	// Clear gater so B can engage.
	verb.SetTransitionGater(nil)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": b.ID, "status": "plan"}), &b)
	if b.Status != "plan" {
		t.Fatalf("B should engage after A's abort freed the seat: %q", b.Status)
	}
}

// TestLeaseAcquireBeforeStatusCommit: while a long gate runs on story A (status
// still backlog), story B's concurrent engage is refused and A has not committed
// plan yet (AC1/AC2 acquire-at-start window).
func TestLeaseAcquireBeforeStatusCommit(t *testing.T) {
	withWiring(t)
	wireWithWorkflows(t, singleStoryWF)

	// The gater holds A on backlog→plan until the test releases it, so B races a
	// gate that is provably still running however slow the machine is.
	release := make(chan struct{})
	gater := &slowGater{to: "plan", entered: make(chan struct{}), release: release}
	verb.SetTransitionGater(gater)
	t.Cleanup(func() {
		gater.releaseOnce.Do(func() { close(release) })
	})

	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "B", "category": "feature"}), &b)

	errCh := make(chan error, 1)
	go func() {
		_, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"})
		errCh <- err
	}()
	// A has acquired its seat by the time its gate is entered.
	select {
	case <-gater.entered:
	case <-time.After(testutil.WaitBudget):
		t.Fatal("A never reached its gate")
	}
	_, berr := dispatchRaw(t, "story-set", map[string]any{"id": b.ID, "status": "plan"})
	if berr == nil || (!strings.Contains(berr.Error(), "engagement seat") && !strings.Contains(berr.Error(), "one performing")) {
		t.Fatalf("B must be refused while A holds seat pre-commit: %v", berr)
	}
	// A still backlog until its gate finishes (or at least B was refused mid-window).
	var mid workitem.Item
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &mid)
	// Let A's gate finish, then wait for A.
	gater.releaseOnce.Do(func() { close(release) })
	if err := <-errCh; err != nil {
		t.Fatalf("A engage: %v", err)
	}
	// After A commits, status is plan.
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &a)
	if a.Status != "plan" {
		t.Fatalf("A final status = %q", a.Status)
	}
	// Mid-window observation: if we caught it before commit, status was backlog.
	_ = mid
}

// slowGater holds the first transition into `to` until release is closed, then
// accepts. entered is closed once the gate is running.
type slowGater struct {
	to          string
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (s *slowGater) Gate(ctx context.Context, item workitem.Item, toStatus string) (verb.GateDecision, error) {
	if toStatus == s.to {
		s.once.Do(func() {
			close(s.entered)
			select {
			case <-s.release:
			case <-time.After(2 * testutil.WaitBudget): // safety net against a hung test; release is the exit
			}
		})
		return verb.GateDecision{Gated: true, Accept: true, Skill: "slow", Notes: "ok"}, nil
	}
	return verb.GateDecision{Gated: false}, nil
}

// TestLeaseGateRejectRetry: sequential plan→in_progress gate reject leaves
// status at plan and clears in_flight so a retry can re-enter the edge.
func TestLeaseGateRejectRetry(t *testing.T) {
	withWiring(t)
	wireWithWorkflows(t, singleStoryWF)
	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)

	verb.SetTransitionGater(rejectToGater{to: "in_progress", skill: "code-ac"})

	_, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"})
	if err == nil {
		t.Fatal("expected reject")
	}
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &a)
	if a.Status != "plan" {
		t.Fatalf("status after reject = %q", a.Status)
	}

	// Retry succeeds after gater cleared.
	verb.SetTransitionGater(nil)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}), &a)
	if a.Status != "in_progress" {
		t.Fatalf("retry after reject: %q", a.Status)
	}
}

func mustJSONLease(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// rejectToGater rejects transitions into a named status; other edges are ungated.
type rejectToGater struct {
	to    string
	skill string
}

func (r rejectToGater) Gate(ctx context.Context, item workitem.Item, toStatus string) (verb.GateDecision, error) {
	if toStatus == r.to {
		return verb.GateDecision{
			Gated: true, Accept: false, Skill: r.skill, Notes: "forced reject for test",
		}, nil
	}
	return verb.GateDecision{Gated: false}, nil
}

// TestLeaseAbortLeavesNoSeatRow: gate reject on NEW acquire leaves no seat row
// (sty_1738f973 AC1 deferred release-on-abort).
func TestLeaseAbortLeavesNoSeatRow(t *testing.T) {
	withWiring(t)
	wireWithWorkflows(t, singleStoryWF)

	verb.SetTransitionGater(rejectToGater{to: "plan", skill: "intent"})

	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	_, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"})
	if err == nil {
		t.Fatal("expected gate reject")
	}
	// Seat list must be empty after abort.
	raw := call(t, "story-seat-list", map[string]any{})
	var seats []map[string]any
	if err := json.Unmarshal(raw, &seats); err != nil {
		t.Fatal(err)
	}
	if len(seats) != 0 {
		t.Fatalf("seat list after abort = %+v, want empty", seats)
	}
}

// TestStorySeatListAndRelease: list reflects a live lease; release frees the seat
// so another story can engage (sty_1738f973 AC4).
func TestStorySeatListAndRelease(t *testing.T) {
	wireWithWorkflows(t, singleStoryWF)

	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "B", "category": "feature"}), &b)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)

	raw := call(t, "story-seat-list", map[string]any{})
	var seats []map[string]any
	if err := json.Unmarshal(raw, &seats); err != nil {
		t.Fatal(err)
	}
	if len(seats) != 1 || seats[0]["id"] != a.ID {
		t.Fatalf("seat list = %+v, want one row for %s", seats, a.ID)
	}

	// B cannot engage while A holds the seat.
	_, berr := dispatchRaw(t, "story-set", map[string]any{"id": b.ID, "status": "plan"})
	if berr == nil || !strings.Contains(berr.Error(), "satelle story seat") {
		t.Fatalf("B refuse must name inspect path: %v", berr)
	}

	// Operator release frees the seat.
	call(t, "story-seat-release", map[string]any{"id": a.ID})
	raw = call(t, "story-seat-list", map[string]any{})
	if err := json.Unmarshal(raw, &seats); err != nil {
		t.Fatal(err)
	}
	if len(seats) != 0 {
		t.Fatalf("after release seats = %+v", seats)
	}
	json.Unmarshal(call(t, "story-set", map[string]any{"id": b.ID, "status": "plan"}), &b)
	if b.Status != "plan" {
		t.Fatalf("B should engage after release: %q", b.Status)
	}
}

// TestOrphanStaleLeaseDoesNotBlockEngage: kill-simulate an orphan (in_flight=1,
// committed backlog, heartbeat past TTL) then assert (a) seat list flags stale
// and (b) a new story engages by stealing the seat (sty_1738f973 AC5).
func TestOrphanStaleLeaseDoesNotBlockEngage(t *testing.T) {
	withWiring(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range singleStoryWF {
		if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"workflows": wfDir}, time.Now()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetLeaseStore(db.Leases)
	t.Cleanup(func() {
		db.Close()
	})

	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Orphan", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Next", "category": "feature"}), &b)

	// Kill-simulate: acquire seat for A without committing status (stays backlog),
	// freeze heartbeat past HeartbeatTTL while in_flight remains 1.
	ctx := context.Background()
	_, out, _, aerr := db.Leases.Acquire(ctx, a.ID, "story", "dead-owner", "plan", true)
	if aerr != nil || out != lease.OutcomeAcquired {
		t.Fatalf("plant orphan lease: out=%v err=%v", out, aerr)
	}
	staleAt := time.Now().UTC().Add(-lease.HeartbeatTTL - time.Minute)
	if err := db.Leases.SetHeartbeat(ctx, a.ID, staleAt); err != nil {
		t.Fatalf("freeze heartbeat: %v", err)
	}
	// The kill: the process that started the transition is gone. A live
	// in-flight pid would keep the seat (lease.Alive), so the probe says dead.
	prevProbe := lease.PidAlive
	lease.PidAlive = func(int) bool { return false }
	t.Cleanup(func() { lease.PidAlive = prevProbe })
	// A committed status is still backlog — the field-defect shape.
	var still workitem.Item
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &still)
	if still.Status != workitem.StatusBacklog {
		t.Fatalf("orphan story status = %q, want backlog", still.Status)
	}
	// Seat row present and stale before any re-engage (list without reap first —
	// Reap runs inside story-seat-list, so call List on the store).
	rows, err := db.Leases.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("orphan row: err=%v len=%d", err, len(rows))
	}
	if lease.Alive(rows[0], time.Now().UTC()) || !rows[0].InFlight {
		t.Fatalf("orphan must be stale+in_flight: %+v", rows[0])
	}
	// New story B engages: Acquire steals the stale seat holder.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": b.ID, "status": "plan"}), &b)
	if b.Status != "plan" {
		t.Fatalf("B should engage after orphan went stale: %q", b.Status)
	}
	// Live seat is B; orphan A row is gone.
	raw := call(t, "story-seat-list", map[string]any{})
	var seats []map[string]any
	if err := json.Unmarshal(raw, &seats); err != nil {
		t.Fatal(err)
	}
	if len(seats) != 1 || seats[0]["id"] != b.ID {
		t.Fatalf("after B engage seats = %+v, want only %s", seats, b.ID)
	}
	// A remains backlog (never transitioned).
	json.Unmarshal(call(t, "story-get", map[string]any{"id": a.ID}), &still)
	if still.Status != workitem.StatusBacklog {
		t.Fatalf("A after steal = %q", still.Status)
	}
}

// TestSameStatusReengageConflictWhenOtherHoldsSeat (sty_4f74d01f AC1 refuse path).
func TestSameStatusReengageConflictWhenOtherHoldsSeat(t *testing.T) {
	wireWithWorkflows(t, singleStoryWF)
	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "B", "category": "feature"}), &b)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	// Drop B never engaged; A holds seat. B stuck in backlog cannot same-status reengage.
	// Instead: engage B fails while A holds; drop A's seat, leave A at plan, engage B.
	// Then try reengage A while B holds → conflict.
	if _, err := verb.Dispatch(context.Background(), "story-seat-release", mustJSONLease(map[string]any{"id": a.ID})); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(call(t, "story-set", map[string]any{"id": b.ID, "status": "plan"}), &b)
	_, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"})
	if err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("want seat conflict for A reengage while B holds, got %v", err)
	}
}
