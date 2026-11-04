//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
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

// sty_ce11bbdb AC1, AC3: a node syncs its work state through the RPCs a local
// satelle uses, to the project its run descriptor names, with its node token as
// the bearer and nothing else changed: the pre-write read is Chain/Read, the
// story rows go by Chain/Append, the execution and its run output by Sync/Apply.
// No call carries a location, no row an origin, and no work state goes over the
// node's HTTP API.
func TestNodeTaskWakeSyncsLikeALocalSatelle(t *testing.T) {
	const project = "acme-team"
	r, subject := taskRig(t, "nightly-sweep", true)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Project = project
	}
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("join: %v\nnode log:\n%s", err, r.log)
	}

	calls := r.sync.rec.all()
	seen := map[string]int{}
	for _, c := range calls {
		seen[c.Method]++
		if c.Project != project || c.Auth != "Bearer "+w.spec.Token || c.Location != "" {
			t.Errorf("call %+v, want project %s, the node token as bearer and no location", c, project)
		}
	}
	for m := range seen {
		if m != "Read" && m != "Append" && m != "Apply" {
			t.Errorf("the node called %s; want only Chain/Read, Chain/Append and Sync/Apply", m)
		}
	}
	if seen["Read"] == 0 || seen["Append"] == 0 || seen["Apply"] != 1 {
		t.Fatalf("calls = %+v, want reads, appends and one apply\nnode log:\n%s", calls, r.log)
	}
	if calls[0].Method != "Read" || calls[0].Story != subject.ID() {
		t.Errorf("first call = %+v, want the pre-write read of the subject %s", calls[0], subject.ID())
	}
	for _, c := range w.fake.Calls() {
		if strings.Contains(c.Path, "/workstate") || strings.Contains(c.Path, "/stories/") {
			t.Errorf("work state went over the node API: %s %s", c.Method, c.Path)
		}
	}

	apply := onePush(t, r)
	exec := executionOf(t, apply)
	if outs := apply.GetOutputs(); len(outs) != 1 || outs[0].GetExecution() != exec || !strings.Contains(string(outs[0].GetBody()), "swept the lot") {
		t.Errorf("outputs = %v, want the run output of %s", outs, exec)
	}
	for _, it := range apply.GetItems() {
		if it.GetOrigin() != "" {
			t.Errorf("item carries origin %q", it.GetOrigin())
		}
	}
	for _, e := range apply.GetLedger() {
		if e.GetOrigin() != "" {
			t.Errorf("ledger row carries origin %q", e.GetOrigin())
		}
	}
	links := 0
	for _, row := range r.sync.appended(t, subject.ID()) {
		if row.Kind == ledger.KindExecutionLink {
			links++
		}
	}
	if links != 1 {
		t.Errorf("execution_link rows appended to the subject = %d, want 1", links)
	}
	for _, req := range r.sync.appends() {
		for _, ev := range req.GetEvents() {
			var payload map[string]any
			if err := json.Unmarshal(ev.GetPayload(), &payload); err != nil {
				t.Fatal(err)
			}
			if _, ok := payload["origin"]; ok {
				t.Errorf("appended event %s carries an origin", ev.GetId())
			}
		}
	}
	if log := r.log.String(); !strings.Contains(log, "published to "+project) {
		t.Errorf("the node's log does not report the publish to %s:\n%s", project, log)
	}
}

// sty_ce11bbdb AC3: the pre-write read refused as the node token's (ended or
// killed node) or as another project's stops the run with the named error, after
// that one RPC: nothing is retried, refreshed, recorded or pushed.
func TestNodeTaskWakeRefusedReadStopsTheRun(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want error
	}{
		{codes.Unauthenticated, hosted.ErrNodeTokenRefused},
		{codes.NotFound, hosted.ErrNodeProjectNotFound},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			r, _ := taskRig(t, "nightly-sweep", true)
			r.sync.rec.refuse = func(rpcCall) error { return status.Error(tc.code, "refused") }
			useTaskNode(t, recordingPerformers(), 0)

			w := r.wake(t)
			err := waitJoin(t, w.joined)
			if !errors.Is(err, tc.want) {
				t.Fatalf("join = %v, want %v\nnode log:\n%s", err, tc.want, r.log)
			}
			if errors.Is(err, hosted.ErrLoginRequired) {
				t.Errorf("join = %v, a node refusal read as a login refusal", err)
			}
			if calls := r.sync.rec.all(); len(calls) != 1 || calls[0].Method != "Read" {
				t.Errorf("calls = %+v, want the one refused read", calls)
			}
			if strings.Contains(r.log.String(), "published to") {
				t.Errorf("the node published after a refused read:\n%s", r.log)
			}
		})
	}
}
