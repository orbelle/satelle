//go:build !windows

package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// hostedSubjectRig is a task rig whose wake names a story of the team workspace
// that the node's store does not hold, as a real node's does not: its row and its
// rows are on the node project's hosted copy only, chained from an empty base.
func hostedSubjectRig(t *testing.T, id string, rows ...ledger.Entry) *nodeRig {
	t.Helper()
	r, _ := taskRig(t, "nightly-sweep", true)
	if err := r.sync.seed(devtest.DefaultProject, id, rows...); err != nil {
		t.Fatal(err)
	}
	r.sync.seedRow(devtest.DefaultProject, id)
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = map[string]string{"mode": "node", "story_id": id, "trigger": "nightly", "trigger_source": "schedule"}
		f.LostAfterSeconds = 3
	}
	return r
}

// onePush is the one Sync/Apply the node made, failing the test unless it made
// exactly one.
func onePush(t *testing.T, r *nodeRig) *syncpb.ApplyRequest {
	t.Helper()
	applies := r.sync.applies()
	if len(applies) != 1 {
		t.Fatalf("the node made %d Sync/Apply calls, want one\nnode log:\n%s", len(applies), r.log)
	}
	return applies[0]
}

// executionOf is the id of the one execution an Apply carries.
func executionOf(t *testing.T, req *syncpb.ApplyRequest) string {
	t.Helper()
	var ids []string
	for _, it := range req.GetItems() {
		var item struct{ ID, Kind string }
		if err := json.Unmarshal(it.GetRecord(), &item); err != nil {
			t.Fatal(err)
		}
		if item.Kind == string(workitem.KindExecution) {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("executions applied = %v, want exactly one", ids)
	}
	return ids[0]
}

// noStaleBase fails the test when the node's log carries a stale-base refusal.
func noStaleBase(t *testing.T, r *nodeRig) {
	t.Helper()
	if log := r.log.String(); strings.Contains(log, "stale base") {
		t.Errorf("the node's log carries a stale-base refusal:\n%s", log)
	}
}

// runRows is the rows a run wrote on its subject, of those the push appended: the
// workflow_version event that records the node's workflow, which any writer's
// first append under a version the chain lacks carries, is set aside. It fails
// the test unless every appended row builds on the one before, the first on head.
func runRows(t *testing.T, r *nodeRig, subject, head string) []pushedRow {
	t.Helper()
	var out []pushedRow
	base := head
	for _, row := range r.sync.appended(t, subject) {
		if row.BaseID != base {
			t.Errorf("pushed %s %s builds on %q, want %q", row.Kind, row.ID, row.BaseID, base)
		}
		base = row.ID
		if row.Kind != ledger.KindWorkflowVersion {
			out = append(out, row)
		}
	}
	return out
}

// sty_0d621eaf AC1, nod_c486006fc01d: a run that stops short of its route writes
// the drive's execution_stopped alert on the subject before its link row, and the
// link row builds on it. With the subject's hosted chain empty, the push carries
// its rows from the empty head, the link row on the alert, and the fake accepts
// them all: no row the push leaves out is the base of one it sends.
func TestNodeTaskWakeStoppedRunEmptyHostedHead(t *testing.T) {
	const subject = "sty_hosted02"
	r := hostedSubjectRig(t, subject)
	useTaskNode(t, &taskPerformers{step: func(workitem.Item) error { return errors.New("the agent crashed") }}, 0)

	w := r.wake(t)
	_ = waitJoin(t, w.joined) // the run did not finish; its push is what is judged
	onePush(t, r)

	rows := runRows(t, r, subject, "")
	if len(rows) != 2 || rows[0].Kind != ledger.KindExecutionStopped || rows[1].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want execution_stopped then execution_link", rows)
	}
	if rows[1].BaseID != rows[0].ID {
		t.Errorf("link base = %q, want the alert %q", rows[1].BaseID, rows[0].ID)
	}
	if n, want := len(r.sync.f.chain.stored(devtest.DefaultProject, subject)), len(r.sync.appended(t, subject)); n != want {
		t.Errorf("the subject's hosted chain has %d rows, want the %d pushed", n, want)
	}
	noStaleBase(t, r)
}
