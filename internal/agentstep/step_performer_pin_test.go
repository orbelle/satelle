package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// pinEngine is an engine over a route with a named-binding step ("named") and an
// executor-allocated one ("exec"), both declaring placement_tag, so a child's pin
// reaches them. The [executor] binding is the given command.
func pinEngine(t *testing.T, namedCommand, executorCommand string) *Engine {
	t.Helper()
	wf := spineWF("", "", "",
		`named|architect|rubric||||placement_tag = "placement:"`,
		`exec|executor|rubric||||placement_tag = "placement:"`,
		"unpinnable|architect|rubric",
		"done")
	g, _ := newEngine(t, "ok", fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "architect" {
			return config.AgentBinding{Command: namedCommand, Tools: "Read"}, true
		}
		return config.AgentBinding{}, false
	})
	g.SetOneShotAgents(func(name string) (config.AgentBinding, bool) {
		if name == "executor" {
			return config.AgentBinding{Command: executorCommand}, true
		}
		return config.AgentBinding{}, false
	})
	return g
}

func pinned(tags ...string) workitem.Item {
	return workitem.Item{ID: "sty_child", Status: "backlog", Tags: tags}
}

// The orchestrator's pin on a child (sty_c931cd90) decides who performs a named
// step: in-loop makes the driving session perform it, local makes a headless
// subagent perform it, team:<path> wakes that remote. Remote is not a pin.
func TestStepPerformerHonoursPin(t *testing.T) {
	g := pinEngine(t, "fake -p {system}", "fake -p {system}")
	ctx := context.Background()

	got, err := g.StepPerformer(ctx, pinned("placement:in-loop"), "named")
	if err != nil || got.Kind != verb.PerformerInLoop || got.Dispatched || got.Agent != "architect" {
		t.Errorf("in-loop pin = %+v, %v; want Kind in-loop, not dispatched, agent architect", got, err)
	}

	got, err = g.StepPerformer(ctx, pinned("placement:local"), "named")
	if err != nil || got.Kind != verb.PerformerDispatched || !got.Dispatched || got.Team != "" || got.Remote || got.Channel {
		t.Errorf("local pin = %+v, %v; want a headless dispatch", got, err)
	}

	got, err = g.StepPerformer(ctx, pinned("placement:team:/teams/x"), "named")
	if err != nil || got.Kind != verb.PerformerDispatched || got.Team != "/teams/x" {
		t.Errorf("team pin = %+v, %v; want a dispatch to team /teams/x", got, err)
	}

	_, err = g.StepPerformer(ctx, pinned("placement:remote"), "named")
	if err == nil || !strings.Contains(err.Error(), "placement:remote") {
		t.Errorf("remote pin: want an error naming the pin, got %v", err)
	}

	// A step that declares no placement_tag ignores a pin.
	got, err = g.StepPerformer(ctx, pinned("placement:in-loop"), "unpinnable")
	if err != nil || got.Kind != verb.PerformerDispatched {
		t.Errorf("pin on a step with no placement_tag = %+v, %v; want the binding's own dispatch", got, err)
	}

	// DispatchExecutor refuses a pin it cannot honour, with the same error.
	if _, err := g.DispatchExecutor(ctx, pinned("placement:bogus"), "named"); err == nil || !strings.Contains(err.Error(), "placement:bogus") {
		t.Errorf("DispatchExecutor on a bad pin: want an error naming it, got %v", err)
	}
}

// sty_1f6e6667 AC4: a child pinned placement:team:<path> on a step whose binding
// is local is woken through that step binding's trigger; with no trigger on the
// binding the wake is refused before any fire, naming the agent, the team
// placement and the key to add.
func TestTeamPinnedChildWakesThroughItsStepBindingsTrigger(t *testing.T) {
	ctx := context.Background()
	child := pinned("placement:team:acme/ops")

	g := pinEngine(t, "fake -p {system}", "fake -p {system}")
	tm := &fakeTeam{statuses: []hosted.RunStanding{concluded("accept")}}
	g.SetTeamRunner(tm)
	_, err := g.DispatchExecutor(ctx, child, "named")
	assertMissingTrigger(t, err, "architect", "team:acme/ops")
	if len(tm.fires) != 0 {
		t.Errorf("fires = %v; a pinned child whose step binding names no trigger must not fire", tm.fires)
	}

	g = pinEngine(t, "fake -p {system}", "fake -p {system}")
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "architect" {
			return config.AgentBinding{Command: "fake -p {system}", Tools: "Read", Trigger: teamTrigger}, true
		}
		return config.AgentBinding{}, false
	})
	tm = &fakeTeam{statuses: []hosted.RunStanding{concluded("accept")}}
	g.SetTeamRunner(tm)
	g.teamClock = &stepClock{}
	if _, err := g.DispatchExecutor(ctx, child, "named"); err != nil {
		t.Fatalf("pinned child with a trigger on its step binding: %v", err)
	}
	if len(tm.fires) != 1 || tm.fires[0] != "acme/ops|sty_child" || tm.triggers[0] != teamTrigger {
		t.Errorf("fires = %v triggers = %v, want one wake of acme/ops for sty_child through %s", tm.fires, tm.triggers, teamTrigger)
	}
}

// A local pin cannot make an in-loop binding headless.
func TestStepPerformerLocalPinOnAnInLoopBindingIsAnError(t *testing.T) {
	g := pinEngine(t, "in-loop", "fake -p {system}")
	if _, err := g.StepPerformer(context.Background(), pinned("placement:local"), "named"); err == nil || !strings.Contains(err.Error(), "pinned local") {
		t.Errorf("want an error naming the local pin, got %v", err)
	}
}

// A step allocated to agent=executor is placed too: unpinned, the [executor]
// binding decides and ExecutorInLoop says which; a pin overrides it.
func TestStepPerformerExecutorStep(t *testing.T) {
	ctx := context.Background()
	inLoop := pinEngine(t, "fake -p {system}", "in-loop")
	headless := pinEngine(t, "fake -p {system}", "fake -p {system}")

	got, err := inLoop.StepPerformer(ctx, pinned(), "exec")
	if err != nil || got.Kind != verb.PerformerExecutor || !got.ExecutorInLoop {
		t.Errorf("unpinned, in-loop [executor] = %+v, %v; want executor with ExecutorInLoop", got, err)
	}
	got, err = headless.StepPerformer(ctx, pinned(), "exec")
	if err != nil || got.Kind != verb.PerformerExecutor || got.ExecutorInLoop {
		t.Errorf("unpinned, headless [executor] = %+v, %v; want executor, not in-loop", got, err)
	}

	got, err = headless.StepPerformer(ctx, pinned("placement:team:/teams/x"), "exec")
	if err != nil || got.Kind != verb.PerformerDispatched || got.Team != "/teams/x" || !got.Dispatched {
		t.Errorf("team pin = %+v, %v; want a dispatch to team /teams/x", got, err)
	}

	got, err = headless.StepPerformer(ctx, pinned("placement:in-loop"), "exec")
	if err != nil || got.Kind != verb.PerformerInLoop {
		t.Errorf("in-loop pin under a headless [executor] = %+v, %v; want in-loop", got, err)
	}

	got, err = headless.StepPerformer(ctx, pinned("placement:local"), "exec")
	if err != nil || got.Kind != verb.PerformerExecutor || got.ExecutorInLoop {
		t.Errorf("local pin under a headless [executor] = %+v, %v; want a headless executor", got, err)
	}
	if _, err := inLoop.StepPerformer(ctx, pinned("placement:local"), "exec"); err == nil || !strings.Contains(err.Error(), "pinned local") {
		t.Errorf("local pin under an in-loop [executor]: want an error naming the pin, got %v", err)
	}
	if _, err := headless.StepPerformer(ctx, pinned("placement:remote"), "exec"); err == nil || !strings.Contains(err.Error(), "placement:remote") {
		t.Errorf("remote pin: want an error naming the pin, got %v", err)
	}
}
