package agentstep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A step whose binding declares placement = "team:<path>" (sty_7028d996) wakes
// that team remote on satelle.dev for the story instead of dispatching a
// performer, then waits for the run the wake started. No command, ACP, cloud or
// channel session opens here: the remote runs its own task with the story as its
// subject, and the step blocks on the run the server holds.

// TeamRunner wakes a team remote through a declared request trigger and reads
// where the story's run stands. The hosted client is the real implementation;
// tests install a fake.
type TeamRunner interface {
	FireRemote(ctx context.Context, remoteID, storyID, trigger string) (hosted.FireResult, error)
	TeamRunStatus(ctx context.Context, remoteID, storyID string) (hosted.RunStanding, error)
}

// SetTeamRunner wires how a placement = "team:<path>" binding wakes its remote.
// Nil refuses such a dispatch, naming the binding: there is no local fallback.
func (g *Engine) SetTeamRunner(r TeamRunner) { g.teamRunner = r }

// teamClock is the time the team wait reads, so a test injects it.
type teamClock interface {
	Now() time.Time
	// Sleep waits d, or until ctx ends.
	Sleep(ctx context.Context, d time.Duration) error
}

type realTeamClock struct{}

func (realTeamClock) Now() time.Time { return time.Now() }

func (realTeamClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// teamOutcomeFailed reports whether a concluded run's outcome is not a success.
// accept, performed and no-op are the outcomes a node reports for a task run it
// carried out; reject is a run that refused the story. A concluded run with no
// outcome is an unreported end — the node ended without reporting one — and fails.
func teamOutcomeFailed(s hosted.RunStanding) bool {
	if s.EndStatus == "killed" || s.EndStatus == "failed" || s.Outcome == "" {
		return true
	}
	return strings.EqualFold(s.Outcome, "reject")
}

// dispatchTeam wakes the team remote at path for the story, through the request
// trigger the step's binding names, and blocks until its run concludes. A binding
// that names no trigger is refused before any fire (sty_1f6e6667): satelle-server
// wakes a runner-backed remote only through a declared request trigger, so the
// error names the key to add. The wait is bounded by the binding's timeout, else the cloud
// dispatch's default deadline (a remote's run is as slow and unattended). It
// returns the run id the status read reports.
func (g *Engine) dispatchTeam(ctx context.Context, item workitem.Item, toStatus, agent, skill, path string,
	binding config.AgentBinding) (verb.DispatchResult, error) {
	res := verb.DispatchResult{Dispatched: true, Agent: agent, Skill: skill, Command: config.PlacementTeamPrefix + path}
	if g.teamRunner == nil {
		return res, fmt.Errorf("binding [%s] is placement = %q but no team remote client is available (is this session signed in to satelle.dev?)",
			agent, config.PlacementTeamPrefix+path)
	}
	deadline, err := binding.TimeoutDuration(g.cloudDefaultDeadline)
	if err != nil {
		return res, fmt.Errorf("named agent %q: invalid timeout in .satelle/workflows/agents.toml [%s]: %w", agent, agent, err)
	}
	clock := g.teamClock
	if clock == nil {
		clock = realTeamClock{}
	}
	placed := config.PlacementTeamPrefix + path
	outcome := "failed"
	runID := ""
	defer func() {
		g.telemetryEvent(ctx, item.ID, "executor", "team_dispatch", map[string]any{
			"agent": agent, "step": toStatus, "placement": placed, "outcome": outcome, "run_id": runID,
		})
	}()
	trigger := strings.TrimSpace(binding.Trigger)
	if trigger == "" {
		return res, fmt.Errorf("named agent %q is placed on %q but its binding names no request trigger; add trigger = \"<request trigger>\" to [%s] in .satelle/workflows/agents.toml (satelle-server wakes a runner-backed remote only through a declared request trigger)",
			agent, placed, agent)
	}

	g.emitActivity(item.ID, "dispatch:"+toStatus, 1, 1)
	if _, err := g.teamRunner.FireRemote(ctx, path, item.ID, trigger); err != nil {
		return res, fmt.Errorf("named agent %q could not wake team remote %q for step %q: %w", agent, path, toStatus, err)
	}
	g.emitProgress("woke team remote %s through trigger %s for %s — waiting up to %s for its run on step %s…", path, trigger, item.ID, deadline, toStatus)

	start := clock.Now()
	for {
		st, err := g.teamRunner.TeamRunStatus(ctx, path, item.ID)
		if err != nil {
			return res, fmt.Errorf("named agent %q reading team remote %q's run for step %q: %w", agent, path, toStatus, err)
		}
		if st.NodeID != "" {
			runID = st.NodeID
			res.RunID = st.NodeID
		}
		if st.State == hosted.RunConcluded {
			if teamOutcomeFailed(st) {
				outcome = "rejected"
				return res, fmt.Errorf("named agent %q: team remote %q's run %s for step %q ended %s",
					agent, path, runLabel(st), toStatus, runEnd(st))
			}
			outcome = "concluded"
			return res, nil
		}
		if !clock.Now().Before(start.Add(deadline)) {
			outcome = "timeout"
			return res, fmt.Errorf("named agent %q: team remote %q's run %s for step %q did not conclude within %s",
				agent, path, runLabel(st), toStatus, deadline)
		}
		if err := clock.Sleep(ctx, g.teamPollEvery()); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				outcome = "canceled"
			}
			return res, fmt.Errorf("named agent %q waiting on team remote %q for step %q: %w", agent, path, toStatus, err)
		}
	}
}

// teamPollEvery is how often the run's standing is read: the cloud dispatch's
// cadence.
func (g *Engine) teamPollEvery() time.Duration {
	if g.cloudPoll > 0 {
		return g.cloudPoll
	}
	return defaultCloudPoll
}

func runLabel(s hosted.RunStanding) string {
	if s.NodeID == "" {
		return "(not yet started)"
	}
	return s.NodeID
}

func runEnd(s hosted.RunStanding) string {
	var parts []string
	if s.EndStatus != "" {
		parts = append(parts, s.EndStatus)
	}
	if s.Outcome != "" {
		parts = append(parts, "with outcome "+s.Outcome)
	}
	if s.Reason != "" {
		parts = append(parts, "("+s.Reason+")")
	}
	if len(parts) == 0 {
		return "unsuccessfully"
	}
	return strings.Join(parts, " ")
}
