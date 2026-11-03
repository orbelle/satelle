//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// recordingPerformers is a task node's performer that records outcome accept with
// an output, as a real performer would.
func recordingPerformers() *taskPerformers {
	return &taskPerformers{step: func(item workitem.Item) error {
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "swept the lot", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}
}

// hostedSubjectRig is a task rig whose wake names a story of the team workspace
// that the node's store does not hold, as a real node's does not: its rows are on
// the fake's hosted chain only, chained from an empty base.
func hostedSubjectRig(t *testing.T, id string, rows ...ledger.Entry) *nodeRig {
	t.Helper()
	r, _ := taskRig(t, "nightly-sweep", true)
	seeded := hostedRows(t, id, rows...)
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = map[string]string{"mode": "node", "story_id": id, "trigger": "nightly", "trigger_source": "schedule"}
		f.LostAfterSeconds = 3
		f.SeedChain(id, seeded...)
	}
	return r
}

// hostedRows chains rows on story id from an empty base, in the encoding a push
// sends.
func hostedRows(t *testing.T, id string, rows ...ledger.Entry) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	base := ""
	for _, e := range rows {
		if e.ID == "" {
			e.ID = ledger.NewID()
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		e.StoryID, e.BaseID = id, base
		raw, err := marshalWorkstateLedger(e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
		base = e.ID
	}
	return out
}

// pushedRow is what a test reads of a pushed ledger row.
type pushedRow struct {
	ID      string          `json:"id"`
	StoryID string          `json:"story_id"`
	Kind    string          `json:"kind"`
	BaseID  string          `json:"base_id"`
	Payload json.RawMessage `json:"payload"`
}

// rowsOn is the ledger rows of b on story, in batch order.
func rowsOn(t *testing.T, b devtest.Batch, story string) []pushedRow {
	t.Helper()
	var out []pushedRow
	for _, raw := range b.Ledger {
		var r pushedRow
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.StoryID == story {
			out = append(out, r)
		}
	}
	return out
}

// onePush is the one batch the fake accepted, failing the test unless the node
// made exactly one push and it was accepted.
func onePush(t *testing.T, r *nodeRig, fake *devtest.Node) devtest.Batch {
	t.Helper()
	var statuses []int
	for _, c := range fake.Calls() {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/workstate") {
			statuses = append(statuses, c.Status)
		}
	}
	pushed := fake.Pushed()
	if len(statuses) != 1 || statuses[0] != http.StatusOK || len(pushed) != 1 {
		t.Fatalf("workstate pushes answered %v, accepted %d, want one accepted push\nnode log:\n%s", statuses, len(pushed), r.log)
	}
	return pushed[0]
}

// executionOf is the id of the execution a batch carries.
func executionOf(t *testing.T, b devtest.Batch) string {
	t.Helper()
	if len(b.Items) != 1 {
		t.Fatalf("items = %d, want exactly the execution", len(b.Items))
	}
	var item struct{ ID string }
	if err := json.Unmarshal(b.Items[0], &item); err != nil || item.ID == "" {
		t.Fatalf("item = %s (%v), want an execution", b.Items[0], err)
	}
	return item.ID
}

// noStaleBase fails the test when the node's log carries a stale-base refusal.
func noStaleBase(t *testing.T, r *nodeRig) {
	t.Helper()
	if log := r.log.String(); strings.Contains(log, "stale base") {
		t.Errorf("the node's log carries a stale-base refusal:\n%s", log)
	}
}

// sty_0d621eaf AC1, nod_c486006fc01d: a run that stops short of its route writes
// the drive's execution_stopped alert on the subject before its link row, and the
// link row builds on it. With the subject's hosted chain empty, the push carries
// the alert on the empty head, then the link row on the alert, and the fake
// accepts both: no row the push leaves out is the base of one it sends.
func TestNodeTaskWakeStoppedRunEmptyHostedHead(t *testing.T) {
	const subject = "sty_hosted02"
	r := hostedSubjectRig(t, subject)
	useTaskNode(t, &taskPerformers{step: func(workitem.Item) error { return errors.New("the agent crashed") }}, 0)

	w := r.wake(t)
	_ = waitJoin(t, w.joined) // the run did not finish; its push is what is judged
	b := onePush(t, r, w.fake)

	rows := rowsOn(t, b, subject)
	if len(rows) != 2 || rows[0].Kind != ledger.KindExecutionStopped || rows[1].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want execution_stopped then execution_link", rows)
	}
	if rows[0].BaseID != "" || rows[1].BaseID != rows[0].ID {
		t.Errorf("bases = %q, %q, want the empty head then the alert %q", rows[0].BaseID, rows[1].BaseID, rows[0].ID)
	}
	if n := len(w.fake.Chain(subject)); n != 2 {
		t.Errorf("the subject's hosted chain has %d rows, want the alert and the link", n)
	}
	noStaleBase(t, r)
}

// hostedRow is a hosted chain event as the node's chain route carries its row: the
// event's payload with the event's id, story and base.
func hostedRow(t *testing.T, ev *syncpb.Event) json.RawMessage {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(ev.GetPayload(), &row); err != nil {
		t.Fatal(err)
	}
	row["id"], row["story_id"], row["base_id"] = ev.GetId(), ev.GetStoryId(), ev.GetBaseId()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
