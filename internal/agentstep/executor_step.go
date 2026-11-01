package agentstep

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/placement"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// SetOneShotAgents wires the resolver PerformExecutorStep uses: name's binding
// through AgentsConfig.EffectiveBinding(UseOneShot) over the raw binding, with
// the section's own baseline default applied first (an unset [executor] command
// is the in-loop session, not a reviewer default). Nil refuses every
// PerformExecutorStep.
func (g *Engine) SetOneShotAgents(fn func(name string) (config.AgentBinding, bool)) {
	g.oneShotAgents = fn
}

// ExecutorBinding is the [executor] binding as a one-shot run would use it, and
// false when no resolver is wired. Its command is "in-loop" when the driving
// session itself performs executor-allocated steps — the case a headless caller
// must refuse rather than perform for it.
func (g *Engine) ExecutorBinding() (config.AgentBinding, bool) {
	if g.oneShotAgents == nil {
		return config.AgentBinding{}, false
	}
	return g.oneShotAgents("executor")
}

// resolveExecutorStep is resolvePerformer's answer for a step allocated to
// agent=executor. Unpinned, whoever the [executor] binding names performs it, and
// ExecutorInLoop says whether that is the driving session. A child pinned by the
// orchestrator (sty_c931cd90) is placed by its pin through placement.Decide: a
// team pin wakes that remote, in-loop pins the driving session, and local pins
// the headless [executor] command — an error when that binding is in-loop, the
// refusal PerformExecutorStep itself gives. A pin that cannot be honoured is an
// error, never a quiet fallback. A missing [executor] resolver counts as in-loop:
// nothing headless can perform the step.
func (g *Engine) resolveExecutorStep(ctx context.Context, item workitem.Item, toStatus string,
	spec wfdot.Spec, wfName string, target *wfdot.State) (performerResolution, error) {
	binding, bound := g.ExecutorBinding()
	inLoop := !bound || config.IsInLoopCommand(binding.CommandTemplate())
	placed := placement.Decide(ctx, item, *target, nil)
	if placed.PinErr != nil {
		return performerResolution{}, fmt.Errorf("step %q: %w", toStatus, placed.PinErr)
	}
	pr := performerResolution{spec: spec, wfName: wfName, target: target, placed: placed, binding: binding}
	pr.Agent = target.Agent
	pr.Team, pr.PlacementNote = placed.Team, placed.Note
	switch {
	case placed.Team != "":
		pr.Kind, pr.Dispatched = verb.PerformerDispatched, true
		pr.Reason = fmt.Sprintf("step %q is allocated to agent=executor and pinned to team remote %s", toStatus, placed.Team)
	case placed.InLoop:
		pr.Kind = verb.PerformerInLoop
		pr.Reason = fmt.Sprintf("step %q is pinned in-loop: the driving session performs it", toStatus)
	case placed.Pinned && inLoop:
		return performerResolution{}, fmt.Errorf(
			"step %q is pinned local but the [executor] command in .satelle/workflows/agents.toml is in-loop: bind a headless CLI, or pin the child in-loop", toStatus)
	default:
		// The route rides on the answer so DispatchExecutor can name the skills
		// the driving session is handed (sty_23ceeca2).
		pr.Kind = verb.PerformerExecutor
		pr.ExecutorInLoop = inLoop
		pr.Reason = fmt.Sprintf("step %q is allocated to agent=%s, which is not a dispatched performer", toStatus, target.Agent)
	}
	return pr, nil
}

// PerformExecutorStep runs the [executor] binding once, headless, to perform the
// work of a step the workflow allocates to agent=executor — the work the driving
// session does by hand when the binding is in-loop. step is the status the story
// is in; its composed executor skills are the rubric, and notes (the reviewers'
// findings from a rejected edge, or "") are appended to it unless the payload
// already carries that exit's findings as findings_to_answer.
//
// It is the executor-allocated counterpart of DispatchExecutor, which performs a
// NAMED agent's step on entry: the same payload, invoke path, transport, budget
// and result record, resolved for the [executor] section. An in-loop binding is
// an error, never a quiet no-op; a performer that says reject comes back as a
// *verb.PerformerReject. It changes no status.
func (g *Engine) PerformExecutorStep(ctx context.Context, item workitem.Item, step, notes string) (verb.DispatchResult, error) {
	spec, wfName, err := g.activeSpec(ctx, item)
	if err != nil {
		return verb.DispatchResult{}, err
	}
	target, ok := spec.StateNamed(step)
	if !ok || target.Agent != "executor" {
		return verb.DispatchResult{}, fmt.Errorf("step %q is not allocated to agent=executor in workflow %q", step, wfName)
	}
	binding, found := g.ExecutorBinding()
	if !found {
		return verb.DispatchResult{}, fmt.Errorf("no agents layer is wired — cannot run the [executor] binding for step %q", step)
	}
	if config.IsInLoopCommand(binding.CommandTemplate()) {
		return verb.DispatchResult{}, fmt.Errorf(
			"step %q is allocated to agent=executor and the [executor] command in .satelle/workflows/agents.toml is in-loop: bind a headless CLI to perform it unattended", step)
	}
	runner, err := g.newRunner(binding.ResolvedInterface(), binding.CommandTemplate())
	if err != nil {
		var enf *ExecutableNotFoundError
		if errors.As(err, &enf) {
			return verb.DispatchResult{}, fmt.Errorf(
				"step %q is performed by the [executor] binding but its command %q is not executable on this machine (%v) — install it or rebind the executor",
				step, enf.Token, enf.Err)
		}
		return verb.DispatchResult{}, fmt.Errorf("[executor]: broken command in .satelle/workflows/agents.toml: %w", err)
	}
	if runner == nil {
		return verb.DispatchResult{}, fmt.Errorf("[executor] command resolves to no runner for step %q", step)
	}
	if err := g.wiringGuard(ctx, "executor", binding.CommandTemplate(), item.ID); err != nil {
		return verb.DispatchResult{}, err
	}
	if config.NeedsContextChannel(binding) && !config.BindingGrantsContextChannel(binding) {
		return verb.DispatchResult{}, fmt.Errorf(
			"the [executor] binding cannot perform step %q: its .satelle/workflows/agents.toml [executor] tools grant has no context channel (add `Bash(satelle:*)` for the satelle CLI, or %s under ~/.satelle/<repo-key>/stories/<id>/)",
			step, agentcli.ContextChannelHint(agentcli.AdapterName(binding.CommandTemplate())))
	}
	composed := spec.ExecutorSkillsFor(step, item.Tags)
	skill := firstStr(composed)
	if skill == "" {
		skill = target.Skill
	}
	delivered, err := g.composeSkills(ctx, composed, false)
	if err != nil {
		return verb.DispatchResult{}, err
	}
	if rerr := g.undeliveredRefusal(ctx, delivered, wfName, step, "executor"); rerr != nil {
		return verb.DispatchResult{Skills: delivered.Manifest, Agent: "executor", Skill: skill}, rerr
	}
	modelResolved, modelSource := g.selectModel(ctx, binding, item.ID, target.Model, config.ModelSourceStep)
	mc := ModelChoice{Model: modelResolved, Source: modelSource}
	timeout, err := binding.TimeoutDuration(g.agentTimeout)
	if err != nil {
		return verb.DispatchResult{}, fmt.Errorf("[executor]: invalid timeout in .satelle/workflows/agents.toml: %w", err)
	}
	idle, err := g.idleTimeoutFor("executor", binding)
	if err != nil {
		return verb.DispatchResult{}, fmt.Errorf("[executor]: invalid idle_timeout in .satelle/workflows/agents.toml: %w", err)
	}
	dlog, sinkPath, closeLog := g.openDispatchLog("executor", item.ID, binding)
	defer closeLog()
	trace := &runTrace{}
	onEvent := trace.handler(dlog.handler())
	g.emitActivity(item.ID, "perform:"+step, 1, 1)
	if sinkPath != "" {
		g.emitProgress("performing step %s with the executor binding (may take several minutes)… live output: %s", step, sinkPath)
	} else {
		g.emitProgress("performing step %s with the executor binding (may take several minutes)…", step)
	}
	invokeBinding := binding
	invokeBinding.Model = mc.Model
	execPayload, consumed := g.performerPayload(ctx, item, step, skill, "executor", binding, target.ApplyCriteriaDoc)
	if consumed != nil {
		g.recordDocConsumed(ctx, item.ID, "executor", step, *consumed, 1)
	}
	rubric := delivered.Rubric
	if n := strings.TrimSpace(notes); n != "" && len(execPayload.Findings) == 0 {
		rubric = strings.TrimSpace(rubric + "\n\n## Findings from the last presentation of this step's exit\n\n" +
			"The reviewers rejected the work this step produced. Address these, then stop:\n\n" + n)
	}
	invRes := g.Invoke(ctx, InvokeRequest{
		Binding:     invokeBinding,
		Section:     "executor",
		Rubric:      rubric,
		Payload:     execPayload,
		Charter:     executorCharter("executor", step, wfName),
		Expect:      ExpectPerform,
		Timeout:     timeout,
		IdleTimeout: idle,
		Runner:      runner,
		OnEvent:     onEvent,
		Log:         dlog,
		StoryID:     item.ID,
		Step:        step,
		Skill:       skill,
		Actor:       "executor",
	})
	res := g.performanceResult("executor", skill, binding, mc, invRes)
	res.Skills = delivered.Manifest
	g.logExecutorRun("executor", item.ID, step, invRes.Stdout, invRes.Err)
	if invRes.Err != nil {
		g.telemetryEvent(ctx, item.ID, "executor", "agent-failure", withCause(map[string]any{
			"agent": "executor", "step": step, "outcome": classifyOutcome(invRes.Err),
			"duration_ms": res.DurationMs, "usage_available": res.UsageAvailable,
		}, invRes.Err))
		return res, fmt.Errorf("the [executor] binding failed performing step %q: %w", step, invRes.Err)
	}
	if aerr := RecordFindingAnswers(&res, execPayload.Findings, execPayload.FindingsEdgeTo, invRes.Stdout); aerr != nil {
		return res, fmt.Errorf("the [executor] binding reported answers for step %q that cannot be read: %w", step, aerr)
	}
	if dec, derr := parseDecision(invRes.Stdout); derr == nil && !dec.Accept {
		return res, &verb.PerformerReject{Notes: dec.Notes}
	}
	g.recordFindingProgress(ctx, &res, item, execPayload.Findings, nil)
	// The executor attaches its documents itself (`satelle story attach`), so the
	// sections it declares are looked for in what was attached since the step was
	// entered as well as in its response; its commands come from this run's own
	// stream, which the record carries to the step's exit gate.
	attached := g.attachedText(ctx, item.ID, g.stepTrace(ctx, item.ID, step))
	res.Evidence = g.dispatchedEvidence(ctx, delivered, binding, string(invRes.Stdout), attached, trace, g.skillEvidenceMode(ctx))
	if len(delivered.evidence) > 0 { // a step that declares nothing leaves no trace row
		res.Trace = trace.record(binding)
	}
	return res, nil
}
