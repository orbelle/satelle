//go:build clislow && !windows

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A concluded task wake pushes its execution, the execution's ledger rows, the
// subject's link row and the run-output doc in one POST to the node's workstate
// route, under the node token, before the concluded heartbeat; the project the
// server answers with is where the fake recorded it.
func TestSlowNodeTaskWakePushesWorkstate(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Project = "acme-team"
	}
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined, spec := w.fake, w.joined, w.spec
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	if n := fake.Count(http.MethodPost, "/workstate"); n != 1 {
		t.Fatalf("workstate pushes = %d, want exactly 1\nnode log:\n%s", n, r.log)
	}
	pushed := fake.Pushed()
	if len(pushed) != 1 {
		t.Fatalf("recorded batches = %d, want 1", len(pushed))
	}
	b := pushed[0]
	if b.Auth != "Bearer "+spec.Token {
		t.Errorf("push Authorization = %q, want the node token as bearer", b.Auth)
	}
	if b.Project != "acme-team" {
		t.Errorf("recorded in project %q, want the one the server answered with", b.Project)
	}

	var exec string
	if len(b.Items) != 1 {
		t.Fatalf("items = %d, want exactly the execution", len(b.Items))
	}
	var item struct{ ID, Kind string }
	if err := json.Unmarshal(b.Items[0], &item); err != nil || item.Kind != string(workitem.KindExecution) {
		t.Fatalf("item = %s (%v), want an execution", b.Items[0], err)
	}
	exec = item.ID

	var links, own int
	for _, raw := range b.Ledger {
		var row struct {
			StoryID string          `json:"story_id"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		switch {
		case row.Kind == ledger.KindExecutionLink:
			links++
			var p ledger.ExecutionLinkPayload
			if err := json.Unmarshal(row.Payload, &p); err != nil {
				t.Fatal(err)
			}
			want := ledger.ExecutionLinkPayload{Execution: exec, Task: "tsk_nightly-sweep", Remote: spec.Session, Outcome: verb.OutcomeAccept}
			if row.StoryID != subject.ID() || p != want {
				t.Errorf("link row = story %q %+v, want story %q %+v", row.StoryID, p, subject.ID(), want)
			}
		case row.StoryID == exec:
			own++
		default:
			t.Errorf("pushed a ledger row that is neither the execution's nor the link: %s", raw)
		}
	}
	if links != 1 || own == 0 {
		t.Errorf("ledger rows: %d link, %d of the execution, want one link and the execution's own", links, own)
	}

	if len(b.Outputs) != 1 || b.Outputs[0].Execution != exec || b.Outputs[0].Name != "output-"+exec+".md" ||
		!strings.Contains(b.Outputs[0].Body, "swept the lot") {
		t.Errorf("outputs = %+v, want the run-output doc of %s", b.Outputs, exec)
	}

	// Pushed after the run and before it was reported concluded and the token ended.
	var order []string
	for _, c := range fake.Calls() {
		switch {
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/workstate"):
			order = append(order, "push")
		case c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status"):
			order = append(order, "status")
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end"):
			order = append(order, "end")
		}
	}
	got := strings.Join(order, " ")
	if !strings.HasSuffix(got, "push status end") {
		t.Errorf("call order = %s, want the push, then the concluded heartbeat, then end", got)
	}
}

// A task node whose root is marked pull-only (sty_07eefabe) runs its task and
// pushes no workstate, saying why.
func TestSlowPullOnlyNodeWorkstatePushSkipped(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	writeRepoFile(t, r.nodeRoot, ".satelle/"+config.LocalConfigName, "[sync]\npull_only = true\n")
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined := w.fake, w.joined
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/workstate"); n != 0 {
		t.Errorf("workstate pushes = %d from a pull-only node, want 0\nnode log:\n%s", n, r.log)
	}
	if log := r.log.String(); !strings.Contains(log, "workstate push skipped") || !strings.Contains(log, "pull_only") {
		t.Errorf("node log does not report the skipped push naming pull_only:\n%s", log)
	}
}

// sty_0d621eaf AC1: a node woken for a story whose hosted chain already has events
// (its head a kill row, as nod_cc5ccae94eef's was) reads that chain under its node
// token before it makes or drives the execution, reads the execution's chain
// before driving it, and its push builds the link row on the hosted head: the fake
// judges each row's base as satelle.dev does, accepts the push and the link row
// becomes the story's hosted head.
func TestSlowNodeTaskWakePushBuildsOnHostedHead(t *testing.T) {
	const subject, kill = "sty_hosted01", "led_kill_nod_916075889ca3"
	r := hostedSubjectRig(t, subject,
		ledger.Entry{Kind: ledger.KindComment, Actor: "bob", Body: "made on the developer's machine"},
		ledger.Entry{ID: kill, Kind: ledger.KindComment, Actor: "satelle", Body: "an earlier node was killed"})
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	b := onePush(t, r, w.fake)
	exec := executionOf(t, b)

	// The subject's chain is read before the execution is driven, the execution's
	// before it is driven, both before the push, and under the node token.
	at := map[string]int{}
	for i, c := range w.fake.Calls() {
		var what string
		switch {
		case c.Method == http.MethodGet && strings.HasSuffix(c.Path, "/stories/"+subject+"/chain"):
			what = "subject-chain"
		case c.Method == http.MethodGet && strings.HasSuffix(c.Path, "/stories/"+exec+"/chain"):
			what = "execution-chain"
		case c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status") && strings.Contains(c.Body, `"run_state":"running"`):
			what = "drive"
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/workstate"):
			what = "push"
		default:
			continue
		}
		if c.Auth != "Bearer "+w.spec.Token {
			t.Errorf("%s %s carried Authorization %q, want the node token", c.Method, c.Path, c.Auth)
		}
		if _, seen := at[what]; !seen {
			at[what] = i
		}
	}
	for _, step := range []string{"subject-chain", "execution-chain", "drive", "push"} {
		if _, ok := at[step]; !ok {
			t.Fatalf("no %s call; calls: %+v", step, w.fake.Calls())
		}
	}
	if !(at["subject-chain"] < at["execution-chain"] && at["execution-chain"] < at["drive"] && at["drive"] < at["push"]) {
		t.Errorf("call order = %v, want the subject's chain, then the execution's, before the drive and the push", at)
	}

	rows := rowsOn(t, b, subject)
	if len(rows) != 1 || rows[0].Kind != ledger.KindExecutionLink {
		t.Fatalf("subject rows pushed = %+v, want only this run's link row", rows)
	}
	if rows[0].BaseID != kill {
		t.Errorf("link row base = %q, want the hosted head %q", rows[0].BaseID, kill)
	}
	chain := w.fake.Chain(subject)
	var head pushedRow
	if err := json.Unmarshal(chain[len(chain)-1], &head); err != nil || head.ID != rows[0].ID || len(chain) != 3 {
		t.Errorf("the subject's hosted chain is %d rows ending %q, want the two seeded and the link row %q", len(chain), head.ID, rows[0].ID)
	}
	noStaleBase(t, r)
	if log := r.log.String(); !strings.Contains(log, "node: workstate: published to "+devtest.DefaultProject) {
		t.Errorf("the node's log does not say the push was published:\n%s", log)
	}
}

// sty_0d621eaf AC2: a node's run reaches the local machine. The developer's
// machine publishes a story; a node woken for it reads that story's hosted chain,
// runs, and pushes; every row the node's push was accepted with is appended to the
// same hosted chain through its compare-and-append rule; and the developer's next
// pull shows the run's execution_link, with its outcome, on the story's ledger,
// the chain unbroken.
func TestSlowNodeRunRowsReadableAfterPull(t *testing.T) {
	ts, f, repo := hostedModeRepo(t)
	subject := createStoryID(t, "Subject of a remote run")
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	hostedChain := func(story string) []*syncpb.Event {
		f.chain.mu.Lock()
		defer f.chain.mu.Unlock()
		return slices.Clone(f.chain.chains[chainKey("probe", story)])
	}
	published := hostedChain(subject)
	if len(published) == 0 {
		t.Fatal("the developer's push left no hosted chain for the subject")
	}
	// The node is another machine: everything of the developer's is put back after it.
	local := map[string]string{}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "SATELLE_HOME", "SATELLE_CONFIG", "SATELLE_SERVER_ENDPOINT"} {
		local[k] = os.Getenv(k)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	r, _ := taskRig(t, "nightly-sweep", true)
	r.fakeSetup = func(nf *devtest.Node) {
		nf.Run = map[string]string{"mode": "node", "story_id": subject, "trigger": "nightly", "trigger_source": "schedule"}
		nf.LostAfterSeconds = 3
		for _, ev := range published {
			nf.SeedChain(subject, hostedRow(t, ev))
		}
		// satelle.dev records an accepted row on the story's hosted chain.
		nf.OnAccept = func(story string, raw json.RawMessage) {
			var row pushedRow
			if err := json.Unmarshal(raw, &row); err != nil {
				t.Error(err)
				return
			}
			var wf struct {
				WorkflowHash string `json:"workflow_hash"`
			}
			_ = json.Unmarshal(raw, &wf)
			ev := &syncpb.Event{Id: row.ID, StoryId: story, BaseId: row.BaseID, WorkflowVersion: wf.WorkflowHash, Payload: raw}
			if _, err := f.chain.Append(context.Background(), &syncpb.AppendRequest{Project: "probe", StoryId: story, BaseId: row.BaseID, Events: []*syncpb.Event{ev}}); err != nil {
				t.Errorf("the hosted chain refused %s of %s: %v", row.ID, story, err)
			}
		}
	}
	useTaskNode(t, recordingPerformers(), 0)
	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	exec := executionOf(t, onePush(t, r, w.fake))
	noStaleBase(t, r)
	// The execution's own rows, its recorded outcome among them, are on its hosted
	// chain beside the subject's link row.
	var outcome bool
	for _, ev := range hostedChain(exec) {
		outcome = outcome || strings.Contains(string(ev.GetPayload()), "outcome "+verb.OutcomeAccept)
	}
	if !outcome {
		t.Errorf("the execution's hosted chain carries no outcome row (%d events)", len(hostedChain(exec)))
	}

	for k, v := range local {
		t.Setenv(k, v)
	}
	t.Chdir(cwd)
	_ = repo
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	out, err := runRoot(t, "ledger", "list", "--story", subject, "--kind", ledger.KindExecutionLink)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	for _, want := range []string{exec, "tsk_nightly-sweep", w.spec.Session, verb.OutcomeAccept} {
		if !strings.Contains(out, want) {
			t.Errorf("the subject's execution_link after the pull does not name %q:\n%s", want, out)
		}
	}
	if out, err := runRoot(t, "ledger", "verify", "--story", subject); err != nil || !strings.Contains(out, "chain ok") {
		t.Errorf("the subject's chain after the pull: %v\n%s", err, out)
	}
}
