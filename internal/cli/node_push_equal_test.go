package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// nodeRun is one task run in the store: its execution (with a recorded run
// output), the subject story it ran about, the subject's execution_link and a
// second row on the subject that is neither an execution's nor a link (the
// evt_8544ab66 shape).
type nodeRun struct{ exec, subject, extra string }

func seedNodeRun(t *testing.T) nodeRun {
	t.Helper()
	subject := createStoryID(t, "Subject of a run")
	db := openScopeDB(t)
	ctx, now := context.Background(), time.Now()
	task, err := db.Stories.Create(ctx, workitem.CreateInput{Kind: workitem.KindTask, Title: "nightly sweep"}, now)
	if err != nil {
		t.Fatal(err)
	}
	exec, err := db.Stories.Create(ctx, workitem.CreateInput{Kind: workitem.KindExecution, Title: "run", ParentID: task.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := json.Marshal(ledger.ExecutionLinkPayload{Execution: exec.ID, Task: task.ID, Remote: "nod_eq", Outcome: verb.OutcomeAccept})
	if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: subject, Kind: ledger.KindExecutionLink, Actor: "remote-node", Payload: link}, now); err != nil {
		t.Fatal(err)
	}
	extra, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: subject, Kind: ledger.KindComment, Actor: "remote-node", Body: "swept the subject"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if out, err := runRoot(t, "execution", "record", exec.ID, "--output", "swept the lot", "--outcome", verb.OutcomeAccept); err != nil {
		t.Fatalf("record: %v\n%s", err, out)
	}
	return nodeRun{exec: exec.ID, subject: subject, extra: extra.ID}
}

// runNodePublish runs nodePublish for run over the store of the current repo,
// with hc as its client, as node nod_eq.
func runNodePublish(t *testing.T, hc *hosted.Client, server, project string, run nodeRun) (string, error) {
	t.Helper()
	_, log, err := runNodePublishReport(t, hc, server, project, "nod_eq", run)
	return log, err
}

// runNodePublishReport is runNodePublish as node nodeID, with the report.
func runNodePublishReport(t *testing.T, hc *hosted.Client, server, project, nodeID string, run nodeRun) (remotenode.PublishReport, string, error) {
	t.Helper()
	restore, _ := verb.SnapshotWiring()
	defer restore()
	var log bytes.Buffer
	cmd := &cobra.Command{Use: "publish"}
	cmd.SetContext(context.Background())
	cmd.SetOut(&log)
	cmd.SetErr(&log)
	if err := openAppForCmd(cmd); err != nil {
		t.Fatal(err)
	}
	defer closeAppForCmd(cmd)
	var exec workitem.Item
	if err := dispatchInto(context.Background(), "story-get", map[string]any{"id": run.exec}, &exec); err != nil {
		t.Fatal(err)
	}
	rep, err := nodePublish(cmd, hc, server, project, nodeID)(context.Background(), exec, run.subject)
	return rep, log.String(), err
}

// nodeClient is a client acting under the node token, as the node builds it.
func nodeClient(server, token string) *hosted.Client {
	return hosted.NewClient(server, hosted.NodeTokenStore{Server: server, Token: token}, nil)
}

// sentRow is one item or ledger row as an Apply carried it.
type sentRow struct{ ID, Record string }

// sentOutput is one run output as an Apply carried it.
type sentOutput struct{ Execution, Name, Body string }

// sentEvent is one event as an Append carried it.
type sentEvent struct{ Project, Story, Base, ID, Workflow, Payload string }

// sent is every payload a server received, apart from its metadata.
type sent struct {
	Items, Ledger []sentRow
	Outputs       []sentOutput
	Events        []sentEvent
	Projects      []string
}

func sentTo(s *nodeSync) sent {
	var out sent
	for _, req := range s.applies() {
		out.Projects = append(out.Projects, req.GetProject())
		for _, it := range req.GetItems() {
			out.Items = append(out.Items, sentRow{it.GetId(), string(it.GetRecord())})
		}
		for _, e := range req.GetLedger() {
			out.Ledger = append(out.Ledger, sentRow{e.GetId(), string(e.GetRecord())})
		}
		for _, o := range req.GetOutputs() {
			out.Outputs = append(out.Outputs, sentOutput{o.GetExecution(), o.GetName(), string(o.GetBody())})
		}
	}
	for _, req := range s.appends() {
		for _, e := range req.GetEvents() {
			out.Events = append(out.Events, sentEvent{req.GetProject(), req.GetStoryId(), e.GetBaseId(), e.GetId(), e.GetWorkflowVersion(), string(e.GetPayload())})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	sort.Slice(out.Ledger, func(i, j int) bool { return out.Ledger[i].ID < out.Ledger[j].ID })
	sort.Slice(out.Outputs, func(i, j int) bool { return out.Outputs[i].Execution < out.Outputs[j].Execution })
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Story < out.Events[j].Story })
	return out
}

// bearers is the distinct authorization each server's calls carried.
func bearers(s *nodeSync) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range s.rec.all() {
		if !seen[c.Auth] {
			seen[c.Auth] = true
			out = append(out, c.Auth)
		}
	}
	return out
}

// sty_ce11bbdb AC2: one run pushed through the node path (nodePublish, node
// token) and, unmodified, through `satelle sync workstate push` scoped to the same
// ids (a member's credential) reaches two servers as the same Apply items, ledger
// and outputs and the same Append events; only the bearer differs. Each server
// accepts every row, the evt_8544ab66-shape row on the subject included.
func TestNodePushEqualsLocalPush(t *testing.T) {
	fx := newHoldFixture(t, appendOnlyToml)
	run := seedNodeRun(t)

	node := newNodeSync(t)
	if log, err := runNodePublish(t, nodeClient("http://node.example", "node-tok"), "http://node.example", "probe", run); err != nil {
		t.Fatalf("node publish: %v\n%s", err, log)
	}

	local := serveNodeSync(t, fx.f)
	if out, err := runRoot(t, "sync", "workstate", "push", "--server", fx.ts.URL, run.exec, run.subject); err != nil {
		t.Fatalf("local push: %v\n%s", err, out)
	}

	gotNode, gotLocal := sentTo(node), sentTo(local)
	if !reflect.DeepEqual(gotNode, gotLocal) {
		t.Errorf("the node and the local push sent different payloads\nnode:  %+v\nlocal: %+v", gotNode, gotLocal)
	}
	if len(gotNode.Outputs) != 1 || gotNode.Outputs[0].Execution != run.exec {
		t.Errorf("outputs = %+v, want the run output of %s", gotNode.Outputs, run.exec)
	}
	if nb, lb := bearers(node), bearers(local); !reflect.DeepEqual(nb, []string{"Bearer node-tok"}) || len(lb) != 1 || lb[0] == nb[0] {
		t.Errorf("bearers: node %v, local %v; want the node token and the member's, each alone", nb, lb)
	}

	for _, s := range []*nodeSync{node, local} {
		stored := s.f.chain.stored("probe", run.subject)
		if len(stored) < 2 || !slices.Contains(stored, run.extra) {
			t.Errorf("the subject's hosted chain is %v, want the link and the evt_8544ab66-shape row %s accepted", stored, run.extra)
		}
		if !s.f.hasItem("probe", run.exec) {
			t.Errorf("the execution %s was not applied", run.exec)
		}
	}
}
