package agentstep

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// ReviewTextRequest is one read-only, one-shot reviewer run that answers in free
// text rather than a gate verdict — the shape `satelle doctor --context-review`
// needs. The rubric and the payload are the caller's; the binding supplies the
// harness, tool grant and model.
type ReviewTextRequest struct {
	// Section is the agents.toml section the binding came from (for messages).
	Section string
	// Binding is the resolved role=reviewer binding to run.
	Binding config.AgentBinding
	// Skill names the rubric for the isolation notes; Rubric is its body.
	Skill  string
	Rubric string
	// Payload is marshalled to JSON and delivered on stdin.
	Payload any
}

// ReviewTextResult is what a ReviewText run produced.
type ReviewTextResult struct {
	Text string
	// Adapter names the provider behind the binding's command — the label an
	// "unavailable" finding carries.
	Adapter string
	// SystemPromptBytes and PayloadBytes are the sizes satelle sent.
	SystemPromptBytes int
	PayloadBytes      int
}

// SetRunnerFactory replaces the constructor a named binding's runner is built
// from. It exists so a caller (and a test) can supply a runner without a real
// agent CLI on PATH; nil keeps the default.
func (g *Engine) SetRunnerFactory(fn func(iface, command string) (agentcli.Runner, error)) {
	if fn != nil {
		g.newRunner = fn
	}
}

// ReviewText runs a read-only reviewer over a rubric and a payload once and
// returns its text. It is the reviewer run path without a work item: the same
// isolation as a gate verdict (the request is marked read-only, the harness
// trims what it offers, a gap is warned and described), the same scratch
// briefing, the same watchdog — and no transition, no ledger row and no
// verdict parse. A binding that is in-loop or not role=reviewer is refused, as
// it would be for a gate.
func (g *Engine) ReviewText(ctx context.Context, req ReviewTextRequest) (ReviewTextResult, error) {
	binding := req.Binding
	section := strings.TrimSpace(req.Section)
	if section == "" {
		section = config.RoleReviewer
	}
	cmd := binding.CommandTemplate()
	adapter := agentcli.AdapterName(cmd)
	res := ReviewTextResult{Adapter: adapter}
	if config.IsInLoopCommand(cmd) {
		return res, fmt.Errorf("[%s] is in-loop — nothing is dispatched, so no reviewer can run", section)
	}
	if role := config.ResolvedRole(section, binding); role != config.RoleReviewer {
		return res, fmt.Errorf("[%s] has role=%q (want role=reviewer)", section, role)
	}
	runner, err := g.newRunner(binding.ResolvedInterface(), cmd)
	if err != nil {
		return res, err
	}
	if runner == nil {
		return res, fmt.Errorf("[%s] resolves to no runner", section)
	}
	scratchDir, err := newScratch(g.repoRoot, "doctor")
	if err != nil {
		return res, err
	}
	defer finishScratch(scratchDir, false)

	built, err := g.buildRequest(ctx, invocation{
		rubric:     req.Rubric,
		principles: config.PrinciplesNone,
		payload:    req.Payload,
		tools:      binding.Tools,
		model:      binding.Model,
		effort:     binding.Effort,
		settings:   binding.Settings,
		env:        overlayScratchEnv(binding.Env, scratchDir),
		scratch:    scratchDir,
	})
	if err != nil {
		return res, err
	}
	res.SystemPromptBytes, res.PayloadBytes = len(built.SystemPrompt), len(built.Payload)
	g.isolateReviewer(ctx, reviewerDispatch{
		Actor: section, Skill: req.Skill, Step: "doctor", Section: section,
	}, runner, binding.OperatorAttested(), &built)

	idle, err := g.idleTimeoutFor(section, binding)
	if err != nil {
		return res, fmt.Errorf("[%s] idle_timeout: %w", section, err)
	}
	busy, err := g.busyTimeoutFor(section, binding)
	if err != nil {
		return res, fmt.Errorf("[%s] busy_timeout: %w", section, err)
	}
	out, _, rerr := g.runOnceBusy(ctx, runner, built, g.agentTimeout, idle, busy)
	if rerr != nil {
		var se *StallError
		if errors.As(rerr, &se) {
			return res, fmt.Errorf("[%s] stalled: no activity for %s (last event: %s)", section, se.Idle, se.LastEvent)
		}
		return res, rerr
	}
	res.Text = strings.TrimSpace(string(out))
	if res.Text == "" {
		return res, fmt.Errorf("[%s] returned no output", section)
	}
	return res, nil
}
