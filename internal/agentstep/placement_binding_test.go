package agentstep

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/verb"
)

// A binding's declared placement (sty_7028d996): placement = "remote" dispatches
// the step through the remote channel, placement = "team:<path>" wakes that team
// remote and waits on its run, and a binding declaring none dispatches as before.
// The channel, the team client and the clock are fakes.

const boundAgent = "worker"

// fakeRemoteRun records the jobs a step sends and answers each with reply.
type fakeRemoteRun struct {
	mu      sync.Mutex
	jobs    []string
	bodies  []remotenode.AgentRequest
	reply   string
	reqErr  error
	endings int
}

func (f *fakeRemoteRun) Request(_ context.Context, jobType string, body any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, jobType)
	if req, ok := body.(remotenode.AgentRequest); ok {
		f.bodies = append(f.bodies, req)
	}
	if f.reqErr != nil {
		return nil, f.reqErr
	}
	return json.Marshal(remotenode.AgentReply{Text: f.reply})
}

func (f *fakeRemoteRun) End() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endings++
	return nil
}

// fakeChannel is a RemoteChannel that opens one fakeRemoteRun.
type fakeChannel struct {
	opens   int
	name    string
	binding config.AgentBinding
	story   string
	err     error
	run     *fakeRemoteRun
}

func (c *fakeChannel) Open(_ context.Context, name string, b config.AgentBinding, story string) (RemoteRun, error) {
	c.opens++
	c.name, c.binding, c.story = name, b, story
	if c.err != nil {
		return nil, c.err
	}
	return c.run, nil
}

// teamTrigger is the request trigger fakeTeam's remote declares.
const teamTrigger = "pr-approve-request"

// fakeTeam is a TeamRunner that records its fires and answers each status read
// with the next scripted standing (the last repeats). Like satelle-server with a
// runner-backed remote (sty_1f6e6667), it refuses a fire that names no declared
// request trigger; its remote declares teamTrigger.
type fakeTeam struct {
	fires    []string
	triggers []string
	fireErr  error
	statuses []hosted.RunStanding
	reads    int
}

func (f *fakeTeam) FireRemote(_ context.Context, remoteID, storyID, trigger string) (hosted.FireResult, error) {
	f.fires = append(f.fires, remoteID+"|"+storyID)
	f.triggers = append(f.triggers, trigger)
	if trigger != teamTrigger {
		return hosted.FireResult{}, &hosted.APIError{Status: 409, Code: "name a declared request trigger to start this remote"}
	}
	return hosted.FireResult{}, f.fireErr
}

func (f *fakeTeam) TeamRunStatus(context.Context, string, string) (hosted.RunStanding, error) {
	i := f.reads
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	f.reads++
	return f.statuses[i], nil
}

// stepClock is a teamClock whose Sleep advances its time, so a deadline is
// reached without waiting.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }
func (c *stepClock) Sleep(_ context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return nil
}

// boundRig is an engine whose step 'plan' is allocated to the binding "worker",
// with a local runner that must not run for a remote or team placement.
type boundRig struct {
	cloudEngine
	local      *fakeRunner
	localBuilt int
	cloud      *fakeCloud
}

func newBoundRig(t *testing.T, b config.AgentBinding) *boundRig {
	t.Helper()
	f := newCloudFix(t)
	r := &boundRig{local: &fakeRunner{out: "did the work"}, cloud: &fakeCloud{}}
	installCloud(t, agentcli.HarnessClaude, r.cloud)
	docs := fakeDocs{workflow: placedWF(""), extraSkills: []docindex.Doc{
		{Kind: "skills", Name: "cloud-step", Body: cloudStepSkill},
		{Kind: "skills", Name: cloudPerformerSkill, Body: cloudPerformerBody},
	}}
	r.cloudEngine = newCloudEngine(t, f, docs, b)
	r.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == boundAgent {
			return b, true
		}
		return config.AgentBinding{}, false
	})
	r.newRunner = func(string, string) (agentcli.Runner, error) {
		r.localBuilt++
		return r.local, nil
	}
	return r
}

func (r *boundRig) ranLocal() bool { return r.local.got.SystemPrompt != "" }

func (r *boundRig) telemetry(kind string) []telemetryRec {
	var out []telemetryRec
	for _, rec := range *r.tele {
		if rec.kind == kind {
			out = append(out, rec)
		}
	}
	return out
}

func remoteBinding() config.AgentBinding {
	return config.AgentBinding{
		Role: "agent", Command: "claude -p {system}", Placement: "remote", Remote: "r1",
	}
}

// AC2: a step allocating a remote binding goes through the remote channel: the
// channel opens once for the binding and story, the job carries the binding, the
// composed step prompt and the payload, the reply is the step's output, the run
// is ended, and the ledger says placement=remote. No local performer runs.
func TestPlacementRemoteBindingDispatchesThroughTheChannel(t *testing.T) {
	r := newBoundRig(t, remoteBinding())
	run := &fakeRemoteRun{reply: "remote did the work"}
	ch := &fakeChannel{run: run}
	r.SetRemoteChannel(ch)

	res, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err != nil {
		t.Fatalf("DispatchExecutor: %v", err)
	}
	if ch.opens != 1 || ch.name != boundAgent || ch.story != cloudStoryID || ch.binding.Remote != "r1" {
		t.Fatalf("channel opened %d times for (%q, %+v, %q), want once for the worker binding of r1 and the story",
			ch.opens, ch.name, ch.binding, ch.story)
	}
	if len(run.jobs) != 1 || run.jobs[0] != remotenode.AgentJob {
		t.Fatalf("jobs = %v, want one %q job", run.jobs, remotenode.AgentJob)
	}
	req := run.bodies[0]
	if req.Binding != boundAgent || !strings.Contains(req.SystemPrompt, "STEP-RUBRIC-BODY") || !strings.Contains(req.Text, cloudStoryID) {
		t.Errorf("request = binding %q, prompt has rubric %v, text has story %v; want the allocated binding, the composed prompt and the payload",
			req.Binding, strings.Contains(req.SystemPrompt, "STEP-RUBRIC-BODY"), strings.Contains(req.Text, cloudStoryID))
	}
	if res.Output != "remote did the work" || res.Agent != boundAgent {
		t.Errorf("result = agent %q output %q, want the remote's text from the worker binding", res.Agent, res.Output)
	}
	if run.endings != 1 {
		t.Errorf("channel ended %d times, want once", run.endings)
	}
	rows := r.telemetry("remote_dispatch")
	if len(rows) != 1 || rows[0].data["placement"] != "remote" {
		t.Errorf("remote_dispatch rows = %+v, want one with placement remote", rows)
	}
	if r.localBuilt != 0 || r.ranLocal() || r.cloud.calls != 0 {
		t.Errorf("a local runner was built %d times (ran: %v) and the cloud launched %d times; a remote placement dispatches neither",
			r.localBuilt, r.ranLocal(), r.cloud.calls)
	}
}

// A remote performer that fails fails the step, and the channel is still ended.
func TestPlacementRemoteBindingFailureEndsTheChannel(t *testing.T) {
	r := newBoundRig(t, remoteBinding())
	run := &fakeRemoteRun{reqErr: errors.New("the node was lost")}
	r.SetRemoteChannel(&fakeChannel{run: run})
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), "the node was lost") {
		t.Fatalf("err = %v, want the remote's failure", err)
	}
	if run.endings != 1 {
		t.Errorf("channel ended %d times, want once", run.endings)
	}
}

// With no channel wired the step refuses naming the binding; it does not fall back
// to a local performer.
func TestPlacementRemoteBindingWithoutAChannelRefuses(t *testing.T) {
	r := newBoundRig(t, remoteBinding())
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), boundAgent) || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("err = %v, want a refusal naming the %q binding and remote placement", err, boundAgent)
	}
	if r.localBuilt != 0 || r.ranLocal() {
		t.Error("a refused remote placement fell back to a local performer")
	}
}

// A channel that cannot open fails the step with the channel's reason.
func TestPlacementRemoteBindingOpenFailure(t *testing.T) {
	r := newBoundRig(t, remoteBinding())
	r.SetRemoteChannel(&fakeChannel{err: errors.New("waiting on satelle-server#sty_e272c46f")})
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), "sty_e272c46f") {
		t.Fatalf("err = %v, want the channel's reason", err)
	}
}

// AC2 / AC5: a binding declaring no placement (or local) dispatches as today —
// the local runner runs and neither the channel nor the team client is touched.
func TestPlacementLocalBindingDispatchesAsToday(t *testing.T) {
	for _, placed := range []string{"", "local"} {
		b := config.AgentBinding{Role: "agent", Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)", Placement: placed}
		r := newBoundRig(t, b)
		ch := &fakeChannel{run: &fakeRemoteRun{}}
		tm := &fakeTeam{statuses: []hosted.RunStanding{{State: hosted.RunConcluded}}}
		r.SetRemoteChannel(ch)
		r.SetTeamRunner(tm)
		if _, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan"); err != nil {
			t.Fatalf("placement %q: %v", placed, err)
		}
		if !r.ranLocal() || ch.opens != 0 || len(tm.fires) != 0 || r.cloud.calls != 0 {
			t.Errorf("placement %q: local ran %v, channel opens %d, team fires %d, cloud calls %d; want the local runner only",
				placed, r.ranLocal(), ch.opens, len(tm.fires), r.cloud.calls)
		}
	}
}

const teamPath = "acme/ops"

func teamBinding(timeout string) config.AgentBinding {
	return config.AgentBinding{Role: "agent", Placement: "team:" + teamPath, Timeout: timeout, Trigger: teamTrigger}
}

func newTeamRig(t *testing.T, timeout string, statuses ...hosted.RunStanding) (*boundRig, *fakeTeam, *stepClock) {
	t.Helper()
	r := newBoundRig(t, teamBinding(timeout))
	tm := &fakeTeam{statuses: statuses}
	clock := &stepClock{now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	r.SetTeamRunner(tm)
	r.teamClock = clock
	return r, tm, clock
}

func concluded(outcome string) hosted.RunStanding {
	return hosted.RunStanding{State: hosted.RunConcluded, NodeID: "nod_9", EndStatus: "ended", Outcome: outcome}
}

// AC3 (a): a team binding wakes its remote for the story, once, and runs no local
// performer, ACP, cloud or channel session; the result carries the run id.
func TestPlacementTeamBindingWakesTheRemote(t *testing.T) {
	r, tm, _ := newTeamRig(t, "", concluded("accept"))
	ch := &fakeChannel{run: &fakeRemoteRun{}}
	r.SetRemoteChannel(ch)

	res, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err != nil {
		t.Fatalf("DispatchExecutor: %v", err)
	}
	if len(tm.fires) != 1 || tm.fires[0] != teamPath+"|"+cloudStoryID {
		t.Fatalf("fires = %v, want one wake of %s for %s", tm.fires, teamPath, cloudStoryID)
	}
	if tm.triggers[0] != teamTrigger {
		t.Errorf("fired through trigger %q, want the binding's %q", tm.triggers[0], teamTrigger)
	}
	if res.RunID != "nod_9" || !res.Dispatched || res.Agent != boundAgent {
		t.Errorf("result = %+v, want a dispatched worker step carrying run nod_9", res)
	}
	if r.localBuilt != 0 || r.ranLocal() || ch.opens != 0 || r.cloud.calls != 0 {
		t.Errorf("local built %d (ran %v), channel opens %d, cloud calls %d; a team wake dispatches no performer",
			r.localBuilt, r.ranLocal(), ch.opens, r.cloud.calls)
	}
	rows := r.telemetry("team_dispatch")
	if len(rows) != 1 || rows[0].data["placement"] != "team:"+teamPath || rows[0].data["run_id"] != "nod_9" {
		t.Errorf("team_dispatch rows = %+v, want one with placement team:%s and the run id", rows, teamPath)
	}
}

// (b): the step waits while the run is queued and running, and returns only after
// the read that finds it concluded.
func TestPlacementTeamBindingWaitsOnTheRun(t *testing.T) {
	r, tm, _ := newTeamRig(t, "",
		hosted.RunStanding{State: hosted.RunQueued},
		hosted.RunStanding{State: hosted.RunRunning, NodeID: "nod_9", Step: "in_progress"},
		concluded("performed"))
	if _, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan"); err != nil {
		t.Fatalf("DispatchExecutor: %v", err)
	}
	if tm.reads != 3 {
		t.Errorf("status reads = %d, want 3 (queued, running, then concluded)", tm.reads)
	}
}

// (c): a run that concludes without success fails the step, naming the run.
func TestPlacementTeamBindingFailedRun(t *testing.T) {
	for name, st := range map[string]hosted.RunStanding{
		"rejected":       concluded("reject"),
		"killed":         {State: hosted.RunConcluded, NodeID: "nod_9", EndStatus: "killed", Reason: "operator"},
		"failed":         {State: hosted.RunConcluded, NodeID: "nod_9", EndStatus: "failed"},
		"unreported end": {State: hosted.RunConcluded, NodeID: "nod_9", EndStatus: "ended", Reason: "node exited"},
	} {
		r, _, _ := newTeamRig(t, "", st)
		_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
		if err == nil || !strings.Contains(err.Error(), "nod_9") {
			t.Errorf("%s: err = %v, want an error naming run nod_9", name, err)
		}
	}
}

// (d): a run that never concludes fails at the binding's timeout, on the injected
// clock, naming the run.
func TestPlacementTeamBindingDeadline(t *testing.T) {
	r, tm, clock := newTeamRig(t, "1s", hosted.RunStanding{State: hosted.RunRunning, NodeID: "nod_9"})
	start := clock.now
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), "nod_9") || !strings.Contains(err.Error(), "within 1s") {
		t.Fatalf("err = %v, want a timeout naming run nod_9 and the 1s bound", err)
	}
	if waited := clock.now.Sub(start); waited < time.Second {
		t.Errorf("gave up after %s on the injected clock, want at least the 1s timeout", waited)
	}
	if tm.reads < 2 {
		t.Errorf("status reads = %d, want it polled until the bound", tm.reads)
	}
}

// With no deadline authored the wait is bounded by the cloud dispatch's default.
func TestPlacementTeamBindingDefaultDeadlineIsTheCloudOne(t *testing.T) {
	r, _, clock := newTeamRig(t, "", hosted.RunStanding{State: hosted.RunRunning})
	r.cloudPoll = 10 * time.Minute
	start := clock.now
	if _, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan"); err == nil {
		t.Fatal("a run that never concludes must time out")
	}
	if waited := clock.now.Sub(start); waited < defaultCloudDeadline {
		t.Errorf("gave up after %s, want at least defaultCloudDeadline %s", waited, defaultCloudDeadline)
	}
}

// (e): no team client wired refuses naming the binding, with no fallback.
func TestPlacementTeamBindingWithoutARunnerRefuses(t *testing.T) {
	r := newBoundRig(t, teamBinding(""))
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), boundAgent) || !strings.Contains(err.Error(), "team:"+teamPath) {
		t.Fatalf("err = %v, want a refusal naming the %q binding and its team placement", err, boundAgent)
	}
	if r.localBuilt != 0 || r.ranLocal() {
		t.Error("a refused team placement fell back to a local performer")
	}
}

// (f): a refused wake (409 limit_spend) fails the step carrying the server's
// outcome, and the run is never polled.
func TestPlacementTeamBindingRefusedWake(t *testing.T) {
	r, tm, _ := newTeamRig(t, "", concluded("accept"))
	tm.fireErr = &hosted.APIError{Status: 409, Code: "limit_spend", Reason: "cap reached"}
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), "limit_spend") {
		t.Fatalf("err = %v, want the server's outcome limit_spend", err)
	}
	if tm.reads != 0 {
		t.Errorf("status reads = %d after a refused wake, want 0", tm.reads)
	}
}

// sty_1f6e6667 AC3: the remote refuses a fire that names no declared request
// trigger, as satelle-server does for a runner-backed remote; a team-bound step
// whose binding names trigger = "pr-approve-request" wakes it through that
// trigger and concludes.
func TestPlacementTeamBindingWakesThroughItsRequestTrigger(t *testing.T) {
	tm := &fakeTeam{statuses: []hosted.RunStanding{concluded("accept")}}
	if _, err := tm.FireRemote(context.Background(), teamPath, cloudStoryID, ""); err == nil ||
		!strings.Contains(err.Error(), "name a declared request trigger") {
		t.Fatalf("fixture: a fire without a trigger must be refused like satelle-server, got %v", err)
	}

	r, tm, _ := newTeamRig(t, "", concluded("accept"))
	if _, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan"); err != nil {
		t.Fatalf("DispatchExecutor: %v", err)
	}
	if len(tm.triggers) != 1 || tm.triggers[0] != "pr-approve-request" {
		t.Fatalf("triggers = %v, want one wake through pr-approve-request", tm.triggers)
	}
	if rows := r.telemetry("team_dispatch"); len(rows) != 1 || rows[0].data["outcome"] != "concluded" {
		t.Errorf("team_dispatch rows = %+v, want one concluded", rows)
	}
}

// sty_1f6e6667 AC4: a team-bound step whose binding names no trigger is refused
// before any fire, naming the agent, its team placement and the trigger key to
// add; the team_dispatch row records the failure.
func TestPlacementTeamBindingWithoutATriggerRefusesBeforeFiring(t *testing.T) {
	b := teamBinding("")
	b.Trigger = ""
	r := newBoundRig(t, b)
	tm := &fakeTeam{statuses: []hosted.RunStanding{concluded("accept")}}
	r.SetTeamRunner(tm)
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	assertMissingTrigger(t, err, boundAgent, "team:"+teamPath)
	if len(tm.fires) != 0 || tm.reads != 0 {
		t.Errorf("fires = %v, reads = %d; a binding without a trigger must not fire", tm.fires, tm.reads)
	}
	if rows := r.telemetry("team_dispatch"); len(rows) != 1 || rows[0].data["outcome"] != "failed" {
		t.Errorf("team_dispatch rows = %+v, want one failed", rows)
	}
}

// assertMissingTrigger checks the AC4 refusal: it names the agent, the team
// placement and the trigger = "<request trigger>" key on that agent's binding.
func assertMissingTrigger(t *testing.T, err error, agent, placed string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a refusal naming the missing trigger, got nil")
	}
	for _, want := range []string{
		`named agent "` + agent + `"`,
		`"` + placed + `"`,
		"names no request trigger",
		`add trigger = "<request trigger>" to [` + agent + `]`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

// (g): a team binding has no command template and is still dispatched — it is not
// resolved as "not a named performer" — and the readiness resolver agrees.
func TestPlacementTeamBindingNeedsNoCommand(t *testing.T) {
	r, _, _ := newTeamRig(t, "", concluded("accept"))
	if teamBinding("").CommandTemplate() != "" {
		t.Fatal("fixture: a team binding must carry no command")
	}
	pr, err := r.StepPerformer(context.Background(), cloudItem(), "plan")
	if err != nil || !pr.Dispatched || pr.Kind != verb.PerformerDispatched || pr.Agent != boundAgent {
		t.Fatalf("StepPerformer = %+v, %v; want the worker binding dispatched", pr, err)
	}
}

// A reviewer-role binding is still refused as a performer, whatever its placement.
func TestPlacementTeamReviewerBindingStillRefused(t *testing.T) {
	b := teamBinding("")
	b.Role = "reviewer"
	r := newBoundRig(t, b)
	r.SetTeamRunner(&fakeTeam{statuses: []hosted.RunStanding{concluded("accept")}})
	_, err := r.DispatchExecutor(context.Background(), cloudItem(), "plan")
	if err == nil || !strings.Contains(err.Error(), "role=reviewer") {
		t.Fatalf("err = %v, want the reviewer-role refusal", err)
	}
}
