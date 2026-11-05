package agentstep

import (
	"context"
	"fmt"
	"sync"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/verb"
)

// reviewerDispatch names one role=reviewer dispatch for the isolation ledger row.
type reviewerDispatch struct {
	StoryID string
	Actor   string // telemetry actor; empty is "reviewer"
	Skill   string
	Step    string
	Section string // the binding's agents.toml section
}

// isolateReviewer is the ONE reviewer tool-isolation path (sty_ef3efb51): every
// role=reviewer dispatch goes through it before any process starts. It REFUSES
// (returns an error, and no process starts) when the runner cannot keep the
// reviewer inside its read-only grant: any preflight gap, an unrecognised
// harness, or a grant naming a write-capable tool (agentcli.ReviewerProblem).
// Otherwise it marks req read-only so the adapter trims what the harness offers
// and denies a tool outside the grant by name, and returns a guard: a runtime
// observation of a tool outside the grant (or a peer that cannot be forced to
// ask) makes guard.err non-nil, and the caller fails the verdict.
func (g *Engine) isolateReviewer(ctx context.Context, d reviewerDispatch, runner agentcli.Runner, req *agentcli.Request) (verb.ToolIsolation, *isolationGuard, error) {
	req.ReadOnly = true
	actor := d.Actor
	if actor == "" {
		actor = "reviewer"
	}
	if err := agentcli.ReviewerProblem(runner, req.AllowedTools); err != nil {
		g.telemetryEvent(ctx, d.StoryID, actor, "reviewer-isolation-refused", map[string]any{
			"skill": d.Skill, "step": d.Step, "agent": d.Section, "reason": err.Error(),
		})
		return verb.ToolIsolation{}, nil, fmt.Errorf("reviewer binding %q refused: %w", d.Section, err)
	}
	iso := toolIsolation(agentcli.DescribeReviewer(runner, *req))

	guard := &isolationGuard{}
	seen := map[string]bool{}
	req.OnIsolation = func(n agentcli.IsolationNote) {
		guard.mu.Lock()
		dup := seen[n.Detail]
		seen[n.Detail] = true
		if guard.first == nil {
			guard.first = fmt.Errorf("reviewer binding %q (%s) left its read-only grant: %s", d.Section, n.Adapter, n.Detail)
		}
		guard.mu.Unlock()
		if dup {
			return
		}
		kind := "reviewer-isolation-gap"
		if n.Breach {
			kind = "reviewer-isolation-breach"
		}
		g.telemetryEvent(ctx, d.StoryID, actor, kind, map[string]any{
			"skill": d.Skill, "step": d.Step, "agent": d.Section, "adapter": n.Adapter, "detail": n.Detail,
		})
		g.warnf("satelle: reviewer binding %q (%s): %s — verdict failed", d.Section, n.Adapter, n.Detail)
	}
	return iso, guard, nil
}

// isolationGuard collects the first runtime isolation observation of one
// reviewer run. A nil guard never fails.
type isolationGuard struct {
	mu    sync.Mutex
	first error
}

// err is the first observation, or nil.
func (g *isolationGuard) err() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.first
}

// warnf writes one warning line to the engine's warning writer (stderr by default).
func (g *Engine) warnf(format string, a ...any) {
	if g.warnOut != nil {
		fmt.Fprintf(g.warnOut, format+"\n", a...)
	}
}

// isolationFields stamps a ToolIsolation onto an agent_invocation payload map.
func isolationFields(m map[string]any, iso verb.ToolIsolation) {
	if iso.OfferedToolCount != nil {
		m["offered_tool_count"] = *iso.OfferedToolCount
	}
	if iso.OfferedToolsSource != "" {
		m["offered_tools_source"] = iso.OfferedToolsSource
	}
	if iso.IsolationLimitation != "" {
		m["isolation_limitation"] = iso.IsolationLimitation
	}
}
