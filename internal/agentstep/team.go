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
// dispatch's default deadline (a remote's run is as slow and unattended). It fires
// every time it is presented and waits only on the run that fire started
// (sty_0b062fb1): a repeat answer naming the story's existing run fails the step
// rather than reporting that run, and it returns the run id of its own node.
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
	// The fire is the first call: no standing is read before it, so an earlier run
	// of the story can never stand in for the run this presentation asks for.
	fired, err := g.teamRunner.FireRemote(ctx, path, item.ID, trigger)
	if err != nil {
		return res, fmt.Errorf("named agent %q could not wake team remote %q for step %q: %w", agent, path, toStatus, err)
	}
	if fired.Repeat() {
		// The server started no run: the story already has one it still counts as
		// live or pending, and it answered with that run. It is not this step's run,
		// so neither its outcome nor a wait on it is taken.
		outcome = "repeat"
		return res, fmt.Errorf("named agent %q: team remote %q answered the wake for step %q with its existing run %s (%s) and started no new run; that run must end before the step can run again",
			agent, path, toStatus, runLabel(*fired.Standing), fired.Standing.State)
	}
	wake := fired.TriggerID
	if wake == "" {
		wake = "(unnamed)"
	}
	if fired.Pending {
		g.emitProgress("wake %s held by team remote %s (position %d) — waiting for its run on step %s for %s, up to %s…",
			wake, path, fired.Position, toStatus, item.ID, deadline)
	} else {
		g.emitProgress("woke team remote %s through trigger %s for %s (trigger %s) — waiting up to %s for its run on step %s…",
			path, trigger, item.ID, wake, deadline, toStatus)
	}

	// ours is the node this wake started. A started wake (200) is recorded only when
	// the story had no live run, and the server writes the node before answering,
	// so the first node a read names is this wake's. A held wake (202) may leave the
	// queue without a node (a refusal on drain), after which the standing falls back
	// to the story's newest ended run; so a held wake pins only a node it first sees
	// running, and a terminal standing seen before that is not taken as its result.
	ours := ""
	sawQueued := false
	start := clock.Now()
	for {
		st, err := g.teamRunner.TeamRunStatus(ctx, path, item.ID)
		if err != nil {
			return res, fmt.Errorf("named agent %q reading team remote %q's run for step %q: %w", agent, path, toStatus, err)
		}
		if ours == "" && fired.Pending {
			switch {
			case st.State == hosted.RunQueued:
				sawQueued = true
			case st.State == hosted.RunRunning && st.NodeID != "":
				ours = st.NodeID
			case st.State == hosted.RunConcluded || (st.State == hosted.RunNone && sawQueued):
				outcome = "held_unstarted"
				return res, fmt.Errorf("named agent %q: team remote %q's held wake %s for step %q left the queue without starting a run this step saw; the story's newest run %s is not taken as its result",
					agent, path, wake, toStatus, runLabel(st))
			}
		} else if ours == "" && st.NodeID != "" {
			ours = st.NodeID
		}
		if ours != "" && st.NodeID == ours {
			runID = ours
			res.RunID = ours
			if st.State == hosted.RunConcluded {
				if teamOutcomeFailed(st) {
					outcome = "rejected"
					return res, fmt.Errorf("named agent %q: team remote %q's run %s for step %q ended %s",
						agent, path, ours, toStatus, runEnd(st))
				}
				outcome = "concluded"
				return res, nil
			}
		}
		if !clock.Now().Before(start.Add(deadline)) {
			outcome = "timeout"
			if ours == "" && fired.Pending {
				return res, fmt.Errorf("named agent %q: team remote %q's wake %s held at position %d for step %q did not start within %s",
					agent, path, wake, fired.Position, toStatus, deadline)
			}
			return res, fmt.Errorf("named agent %q: team remote %q's run %s for step %q did not conclude within %s",
				agent, path, runLabel(hosted.RunStanding{NodeID: ours}), toStatus, deadline)
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
