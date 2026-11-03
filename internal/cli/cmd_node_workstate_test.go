//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode"
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

// The push needs no satelle login: the node's home holds no stored credentials and
// no [sync] project is bound, yet the run is pushed, and every request the fake saw
// carried only the node token.
func TestNodeWorkstatePushNeedsNoLogin(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined, spec := w.fake, w.joined, w.spec
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := len(fake.Pushed()); n != 1 {
		t.Fatalf("pushes = %d, want 1 without any login\nnode log:\n%s", n, r.log)
	}
	for _, c := range fake.Calls() {
		if c.Auth != "Bearer "+spec.Token {
			t.Errorf("%s %s carried Authorization %q, want only the node token", c.Method, c.Path, c.Auth)
		}
	}
	// Nothing of a login was stored in the empty home, and the node's own config
	// binds no project.
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("the node wrote %s under its home", p)
		}
		return nil
	})
	if cfg, err := os.ReadFile(filepath.Join(r.nodeRoot, ".satelle", "satelle.toml")); err == nil {
		for _, ln := range strings.Split(string(cfg), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "project") {
				t.Errorf("the node's config binds a project: %q", ln)
			}
		}
	}
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

// sty_0d621eaf AC1: the subject's hosted chain already carries an earlier run's
// execution_link. The fetch brings it into the node's store, and the push sends
// only this run's link row, built on it, never the fetched one again.
func TestNodeTaskWakeDoesNotRepushFetchedLink(t *testing.T) {
	const subject = "sty_hosted03"
	earlier, err := json.Marshal(ledger.ExecutionLinkPayload{Execution: "exe_earlier01", Task: "tsk_nightly-sweep", Remote: "nod_earlier", Outcome: verb.OutcomeAccept})
	if err != nil {
		t.Fatal(err)
	}
	const earlierID = "evt_earlierlink"
	r := hostedSubjectRig(t, subject,
		ledger.Entry{Kind: ledger.KindComment, Actor: "bob", Body: "raised"},
		ledger.Entry{ID: earlierID, Kind: ledger.KindExecutionLink, Actor: "remote-node", Payload: earlier})
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	b := onePush(t, r, w.fake)
	exec := executionOf(t, b)

	rows := rowsOn(t, b, subject)
	if len(rows) != 1 || rows[0].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want exactly this run's link row", rows)
	}
	var p ledger.ExecutionLinkPayload
	if err := json.Unmarshal(rows[0].Payload, &p); err != nil || p.Execution != exec {
		t.Errorf("the pushed link names execution %q (%v), want this run's %q", p.Execution, err, exec)
	}
	if rows[0].BaseID != earlierID {
		t.Errorf("link row base = %q, want the earlier run's link %q", rows[0].BaseID, earlierID)
	}
	noStaleBase(t, r)
}

// sty_0d621eaf: a satelle.dev that serves no chain to a node (404) does not block
// the run. The node says the chain was not read, writes on what its store holds,
// concludes and still pushes.
func TestNodeTaskWakeChainRouteMissingStillPushes(t *testing.T) {
	const subject = "sty_hosted04"
	r := hostedSubjectRig(t, subject)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.NoChainRoute = true
	}
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	log := r.log.String()
	for _, want := range []string{"the hosted chain of " + subject + " was not read before this write", remotenode.ErrChainRouteUnsupported.Error()} {
		if !strings.Contains(log, want) {
			t.Errorf("the node's log does not say %q:\n%s", want, log)
		}
	}
	onePush(t, r, w.fake)
	st := w.fake.Statuses()
	if last := st[len(st)-1]; last["run_state"] != "concluded" || last["outcome"] != verb.OutcomeAccept {
		t.Errorf("last heartbeat = %+v, want concluded/accept", last)
	}
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
