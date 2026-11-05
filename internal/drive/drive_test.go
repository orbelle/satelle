package drive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// env is the verb layer wired to a real store over a fixture route. The gates
// are scripted (scriptedGater); who performs what is whatever the test hands
// drive.Story.
type env struct {
	t     *testing.T
	db    *store.DB
	dir   string
	gater *scriptedGater
}

// scriptedGater rejects an edge's first rejects[to] presentations with notes,
// then accepts; always[to] rejects it every time.
type scriptedGater struct {
	mu      sync.Mutex
	rejects map[string]int
	always  map[string]bool
	seen    map[string]int
	// errs fails the gate itself (not a rejection) on that edge.
	errs map[string]error
	// onGate runs before each verdict, outside the lock.
	onGate func(to string)
}

func (g *scriptedGater) Gate(_ context.Context, _ workitem.Item, to string) (verb.GateDecision, error) {
	if g.onGate != nil {
		g.onGate(to)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen[to]++
	if err := g.errs[to]; err != nil {
		return verb.GateDecision{}, err
	}
	if g.always[to] || g.seen[to] <= g.rejects[to] {
		return verb.GateDecision{Gated: true, Accept: false, Skill: "stub-gate", Notes: fmt.Sprintf("OBJECTION-%d to %s", g.seen[to], to)}, nil
	}
	return verb.GateDecision{Gated: true, Accept: true, Skill: "stub-gate"}, nil
}

const routeDone = `["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`

func writeRoute(t *testing.T, dir, done, step string) {
	t.Helper()
	write := func(name, body string) {
		doc := "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"fixture\"\n\n" + body
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("done", done)
	write("step", testutil.UngatedStep(step))
}

func newEnv(t *testing.T, done, step string) *env {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	restore, _ := verb.SnapshotWiring()
	t.Cleanup(restore)
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir, done, step)
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"workflows": wfDir}, time.Now()); err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetLeaseStore(db.Leases)
	verb.SetStoryDir(filepath.Join(dir, "stories"))
	g := &scriptedGater{rejects: map[string]int{}, always: map[string]bool{}, seen: map[string]int{}, errs: map[string]error{}}
	verb.SetTransitionGater(g)
	return &env{t: t, db: db, dir: dir, gater: g}
}

func (e *env) story() string {
	e.t.Helper()
	raw, err := verb.Dispatch(context.Background(), "story-create", mustJSON(map[string]any{
		"title": "drive me", "body": "goal", "acceptance_criteria": "1. ok", "category": "chore",
	}))
	if err != nil {
		e.t.Fatalf("story-create: %v", err)
	}
	var it workitem.Item
	if err := json.Unmarshal(raw, &it); err != nil || it.ID == "" {
		e.t.Fatalf("story-create: %v %q", err, raw)
	}
	return it.ID
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (e *env) status(id string) string {
	e.t.Helper()
	it, err := e.db.Stories.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return it.Status
}

func (e *env) rows(id, kind string) []ledger.Entry {
	e.t.Helper()
	es, err := e.db.Ledger.ListByStory(context.Background(), id, kind)
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

// fakePerformers is a verb.PerformerResolver and executor runner scripted per
// step, so a test can say who performs what without an agents.toml.
type fakePerformers struct {
	kinds     map[string]verb.PerformerKind
	agents    map[string]string
	executor  config.AgentBinding
	onPerform func(item workitem.Item, step, notes string) error
	// output is what the executor run printed last.
	output string
}

func (f *fakePerformers) StepPerformer(_ context.Context, _ workitem.Item, step string) (verb.PerformerResolution, error) {
	k, ok := f.kinds[step]
	if !ok {
		k = verb.PerformerNone
	}
	return verb.PerformerResolution{
		Kind: k, Agent: f.agents[step], Dispatched: k == verb.PerformerDispatched,
		ExecutorInLoop: k == verb.PerformerExecutor && config.IsInLoopCommand(f.executor.CommandTemplate()),
	}, nil
}

func (f *fakePerformers) PerformExecutorStep(_ context.Context, item workitem.Item, step, notes string) (verb.DispatchResult, error) {
	res := verb.DispatchResult{Dispatched: true, Agent: "executor", Command: "fake-executor", Skill: "s", Output: f.output, LastMessage: verb.LastMessage(f.output)}
	if f.onPerform != nil {
		return res, f.onPerform(item, step, notes)
	}
	return res, nil
}

const workRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`

func headless() config.AgentBinding { return config.AgentBinding{Command: "fake-cli -p {system}"} }

// AC6: drive.Story takes no harness. Handed a real engine whose [executor] is a
// grok-shaped command, it walks a story to its terminal step and the grok
// binding is what performed the step.
func TestStoryDrivesAGrokShapedBindingToTheTerminalStep(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	script := filepath.Join(t.TempDir(), "grok")
	log := filepath.Join(t.TempDir(), "grok.log")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n{ echo \"=== ARGV: $*\"; cat; } >> "+log+"\necho performed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := agentstep.New(nil, e.db.DocIndex, e.dir, "")
	eng.SetOneShotAgents(func(name string) (config.AgentBinding, bool) {
		if name != "executor" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{
			Role:    config.RoleAgent,
			Command: script + " -p {payload} --system-prompt-override {system} --tools read_file,grep,list_dir --output-format plain",
			Tools:   "read_file,grep,list_dir,write",
		}, true
	})
	id := e.story()
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: eng})
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if !res.Terminal || res.Parked || res.Status != "done" || e.status(id) != "done" {
		t.Fatalf("want the story done, got %+v (stored %q)", res, e.status(id))
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the grok-shaped binding never ran: %v", err)
	}
	if !strings.Contains(string(body), "--system-prompt-override") || !strings.Contains(string(body), id) {
		t.Errorf("the binding must be run as its own command line with the story payload:\n%s", body)
	}
	var invoked bool
	for _, row := range e.rows(id, ledger.KindAgentInvocation) {
		invoked = invoked || strings.Contains(row.Body, `performed step "in_progress"`)
	}
	if !invoked {
		t.Errorf("the headless run must be recorded as an agent_invocation row")
	}
	if got := len(res.Moves); got != 2 {
		t.Errorf("moves = %+v, want one per edge", res.Moves)
	}
	e.wantNoDriveRecords(id)
}

// AC5: inside the drive's process every gate runs in the foreground (the story is
// at the next status as soon as Story returns) and the seat lease and the ledger
// carry the resolved session, not a fresh one.
func TestStoryRunsUnderTheResolvedSession(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	t.Setenv(config.SessionEnv, "sess-drive-test")
	t.Setenv("SATELLE_OWNER", "owner-drive-test")
	id := e.story()
	var held lease.Lease
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(item workitem.Item, _, _ string) error {
			l, err := e.db.Leases.Get(context.Background(), item.ID)
			if err != nil {
				return fmt.Errorf("the seat must be held while the step is performed: %w", err)
			}
			held = l
			return nil
		},
	}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil || !res.Terminal {
		t.Fatalf("drive: %v %+v", err, res)
	}
	if want := config.ResolveSession(); want != "sess-drive-test" || held.SessionID != want {
		t.Errorf("lease session = %q, want the resolved session %q", held.SessionID, want)
	}
	if held.Owner != lease.ResolveOwner() || held.Owner != "owner-drive-test" {
		t.Errorf("lease owner = %q, want %q", held.Owner, lease.ResolveOwner())
	}
	var carrying int
	for _, row := range e.rows(id, "") {
		if strings.Contains(string(row.Payload), "sess-drive-test") {
			carrying++
		}
	}
	if carrying == 0 {
		t.Errorf("no ledger row carries the resolved session")
	}
	if e.gater.seen["in_progress"] != 1 || e.gater.seen["done"] != 1 {
		t.Errorf("each edge's gate ran once, in this process: %v", e.gater.seen)
	}
}

// AC2: an in-loop [executor] is refused before any transition, naming the key.
func TestStoryRefusesAnInLoopExecutorBeforeAnyTransition(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	id := e.story()
	before := e.ledgerIDs()
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: config.AgentBinding{Command: "in-loop"},
	}
	_, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err == nil || !strings.Contains(err.Error(), `step "in_progress"`) || !strings.Contains(err.Error(), "[executor] command") {
		t.Fatalf("want a refusal naming the step and [executor] command, got %v", err)
	}
	if e.status(id) != "backlog" || len(e.rows(id, ledger.KindStatusTransition)) != 0 {
		t.Errorf("a refused drive moves nothing: %q", e.status(id))
	}
	e.wantLedgerDelta(before)
}

// AC3: a relay that converges re-presents the edge, which the gate then accepts.
func TestStoryRepresentsTheEdgeAfterAConvergedRelay(t *testing.T) {
	e := newEnv(t, routeDone, `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
rework = { consult = "reviewer-consult", rounds = 2 }
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	e.gater.rejects["done"] = 1
	id := e.story()
	perf := &fakePerformers{
		kinds:  map[string]verb.PerformerKind{"in_progress": verb.PerformerDispatched},
		agents: map[string]string{"in_progress": "coder"},
	}
	relays := 0
	res, err := drive.Story(context.Background(), id, drive.Options{
		Performers: perf,
		Rework: func(context.Context, string) (drive.Rework, error) {
			relays++
			return drive.Rework{Converged: true, Outcome: "converged", Rounds: 1}, nil
		},
	})
	if err != nil || !res.Terminal || relays != 1 || e.gater.seen["done"] != 2 {
		t.Fatalf("want done after one relay and two presentations: %v %+v relays=%d seen=%v", err, res, relays, e.gater.seen)
	}
}

// AC4: an executor that rejects the step parks the story with its notes.
func TestStoryParksWhenTheExecutorRejectsItsStep(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	id := e.story()
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(workitem.Item, string, string) error {
			return &verb.PerformerReject{Notes: "merge conflict in main.go"}
		},
	}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if !res.Parked || e.status(id) != "blocked" || !strings.Contains(res.Reason, "merge conflict in main.go") {
		t.Fatalf("want parked with the executor's notes, got %+v (stored %q)", res, e.status(id))
	}
}

// An executor run that fails after the first presentation parks the story with the
// failure as the reason, naming the step and the executor.
func TestStoryParksWhenTheExecutorFails(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	id := e.story()
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(workitem.Item, string, string) error { return fmt.Errorf("rate limited") },
	}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil {
		t.Fatalf("a failed run parks, it is not an error: %v", err)
	}
	if !res.Parked || e.status(id) != "blocked" || !strings.HasPrefix(res.Reason, "step in_progress, performer executor:") || !strings.Contains(res.Reason, "rate limited") {
		t.Errorf("want parked naming the step, the executor and the failure, got %+v (stored %q)", res, e.status(id))
	}
}

// An edge whose step declares no reject_budget is still bounded.
func TestStoryParksAnUnbudgetedEdgeAfterTheDriveCap(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.gater.always["in_progress"] = true
	id := e.story()
	perf := &fakePerformers{}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if !res.Parked || e.gater.seen["in_progress"] != 10 || !strings.Contains(res.Reason, "OBJECTION-10") {
		t.Fatalf("want parked after the cap with the last objection, got %+v seen=%v", res, e.gater.seen)
	}
}

// A story already at a terminal step or a park is reported, never moved.
func TestStoryReportsAStoryThatIsAlreadyStopped(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	id := e.story()
	perf := &fakePerformers{}
	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf}); err != nil {
		t.Fatal(err)
	}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil || !res.Terminal || res.Status != "done" || len(res.Moves) != 0 {
		t.Fatalf("a done story is reported, not moved: %v %+v", err, res)
	}
}

// A drive walks an execution of a task as it walks a story, and OnMove reports
// each move as it is made.
func TestStoryDrivesAnExecutionAndReportsEachMove(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	ctx := context.Background()
	create := func(name string, req map[string]any) workitem.Item {
		t.Helper()
		raw, err := verb.Dispatch(ctx, name, mustJSON(req))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var it workitem.Item
		if err := json.Unmarshal(raw, &it); err != nil || it.ID == "" {
			t.Fatalf("%s: %v %q", name, err, raw)
		}
		return it
	}
	task := create("task-create", map[string]any{"title": "sweep"})
	exec := create("execution-create", map[string]any{"title": "one sweep", "parent_id": task.ID})
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	var seen []drive.Move
	res, err := drive.Story(ctx, exec.ID, drive.Options{Performers: perf, OnMove: func(m drive.Move) { seen = append(seen, m) }})
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if !res.Terminal || res.Parked || e.status(exec.ID) != "done" {
		t.Fatalf("want the execution done, got %+v (stored %q)", res, e.status(exec.ID))
	}
	if len(seen) != 2 || len(seen) != len(res.Moves) || seen[0] != res.Moves[0] || seen[1].To != "done" {
		t.Errorf("OnMove saw %+v, want the drive's own moves %+v", seen, res.Moves)
	}
}

func TestStoryRequiresIDAndPerformers(t *testing.T) {
	if _, err := drive.Story(context.Background(), " ", drive.Options{Performers: &fakePerformers{}}); err == nil {
		t.Error("an empty id must be refused")
	}
	if _, err := drive.Story(context.Background(), "sty_x", drive.Options{}); err == nil {
		t.Error("a drive with no performer resolver must be refused")
	}
}
