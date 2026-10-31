package agentstep

import (
	"errors"
	"sync"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/verb"
)

// toolTally counts the tool-start events one reviewer run's transport reported.
// The verdict retry loop zeroes it before each run, so the count matches the
// usage of the run that produced the verdict (or the last failed one).
type toolTally struct {
	mu sync.Mutex
	n  int
}

func (t *toolTally) add() {
	t.mu.Lock()
	t.n++
	t.mu.Unlock()
}

func (t *toolTally) reset() {
	t.mu.Lock()
	t.n = 0
	t.mu.Unlock()
}

func (t *toolTally) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

// counts is what the run measured about its own work: the turns its output
// reported and the tool calls its transport emitted events for. A figure the
// harness did not give is recorded with the adapter-named reason and never as a
// zero; a transport that does emit tool events yields a measured count, zero
// included (agentcli.ReportsToolEvents).
func (t *toolTally) counts(u agentcli.UsageResult, iface, command string) verb.RunCounts {
	c := verb.RunCounts{}
	c.Turns, c.TurnsUnavailableReason = turnsOf(u, iface, command)
	if agentcli.ReportsToolEvents(iface, command) {
		n := t.count()
		c.ToolCalls = &n
	} else {
		c.ToolCallsUnavailableReason = agentcli.ToolCallsUnavailableReason(iface, command)
	}
	return c
}

// turnsOf is the run's reported turn count, or nil with the adapter-named reason
// the run reported none — the one rule a budget report and a reviewer row share.
func turnsOf(u agentcli.UsageResult, iface, command string) (*int, string) {
	if u.TurnsAvailable {
		n := u.Turns
		return &n, ""
	}
	return nil, agentcli.TurnsUnavailableReason(iface, command)
}

// failureOutcome classifies a reviewer attempt that produced no verdict:
// verb.OutcomeMaxTurns when the harness stopped it at its turn cap, otherwise
// verb.OutcomeUnavailable.
func failureOutcome(err error) string {
	if errors.Is(err, agentcli.ErrMaxTurns) {
		return verb.OutcomeMaxTurns
	}
	return verb.OutcomeUnavailable
}

// failureStamp is the invocation record of a reviewer attempt that started but
// produced no verdict: its command, usage, turns and tool calls, and why it
// ended. The round writer appends it as an agent_invocation row.
func (g *Engine) failureStamp(skill string, res InvokeResult, seat reviewerSeat) verb.ReviewerVerdict {
	var d verb.GateDecision
	// The same usage mapping and stamp a judged attempt goes through, so a failed
	// attempt's tokens, duration and isolation are recorded exactly as a judged
	// one's are.
	g.setDecisionUsage(&d, res.Usage, res.RanModel(seat.binding.Model))
	stampInvocation(&d, res, seat)
	d.Command, d.Context, d.RunCounts = res.Command, skill, res.Counts
	rv := reviewerVerdictOf(skill, 0, false, d)
	rv.Outcome = failureOutcome(res.Err)
	return rv
}
