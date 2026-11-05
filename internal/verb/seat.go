package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
)

func init() {
	Register(&Verb{
		Name:        "story-seat-list",
		Description: "List engagement seat leases (reaps stale rows first)",
		Invoke:      storySeatList,
	})
	Register(&Verb{
		Name:        "story-seat-release",
		Description: "Force-release an engagement seat by story/task id (operator override)",
		Invoke:      storySeatRelease,
	})
}

// seatRow is one engagement_lease row for the agent/operator surface.
type seatRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	StorySeat bool   `json:"story_seat"`
	// SeatKey is the arbitration key the seat is held under, and Worktree the
	// tree the engagement is anchored to (sty_c098dc2d). Rows are listed in
	// seat_key order, so co-holders of one key appear together — that grouping
	// IS the epic view; the JSON stays a flat array carrying the key per row.
	SeatKey       string `json:"seat_key,omitempty"`
	Worktree      string `json:"worktree,omitempty"`
	Owner         string `json:"owner"`
	State         string `json:"state"`
	InFlight      bool   `json:"in_flight"`
	AcquiredAge   string `json:"acquired_age"`
	HeartbeatAge  string `json:"heartbeat_age"`
	Stale         bool   `json:"stale"`
	StopRequested string `json:"stop_requested_by,omitempty"`
	StopReason    string `json:"stop_reason,omitempty"`
	// Activity fields — queryable gate progress while in_flight (sty_598a8e1b).
	// Omitted when the transition is not live or no activity has been stamped.
	Activity      string `json:"activity,omitempty"`
	ActivityIndex int    `json:"activity_index,omitempty"`
	ActivityTotal int    `json:"activity_total,omitempty"`
	ActivityAge   string `json:"activity_age,omitempty"`
	// ActivityGates marks a gate round's stamp, whose ActivityPassed and
	// ActivityFailed count the round's gates returned so far (sty_216d5205).
	ActivityGates  bool `json:"activity_gates,omitempty"`
	ActivityPassed int  `json:"activity_passed,omitempty"`
	ActivityFailed int  `json:"activity_failed,omitempty"`
	// In-flight DISPATCH metadata (sty_752c4ef2) — which agent/model/pid is
	// running the dispatch, the last real event it produced, and this repo's
	// configured idle-stall bound for that binding. Omitted when Activity is
	// empty (no dispatch has stamped detail on this lease yet).
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	Pid          int    `json:"pid,omitempty"`
	LastEvent    string `json:"last_event,omitempty"`
	LastEventAge string `json:"last_event_age,omitempty"`
	EventCount   int    `json:"event_count,omitempty"`
	IdleTimeout  string `json:"idle_timeout,omitempty"`
}

// storySeatList reaps stale leases then lists remaining rows with age/stale flags
// (sty_1738f973 AC4). Reap is opportunistic honesty; gate correctness does not
// depend on it. Each reaped story-seat lease also gets a final driver_usage
// row (sty_81caa41b AC5): a driving session found dead here never ran its own
// park/close snapshot, so this is the only place that usage and wall time
// would ever get recorded. Also sweeps any session whose latest driver_usage
// row is still Pending confirmation (AC6) — the general "session's tail never
// gets swept after its last close" case, not just a dead one: there is no
// daemon in this binary, so this frequently-invoked command is where it runs.
func storySeatList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	ls, err := requireLease()
	if err != nil {
		return nil, err
	}
	_, err = ls.Reap(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	all, err := ls.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]seatRow, 0, len(all))
	for _, l := range all {
		out = append(out, seatRowFromLease(l, now))
	}
	return json.Marshal(out)
}

// storySeatRelease force-releases a seat regardless of owner (operator/agent
// recovery for an orphaned or stuck holder — sty_1738f973 AC4).
func storySeatRelease(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	ls, err := requireLease()
	if err != nil {
		return nil, err
	}
	if err := ls.ForceRelease(ctx, req.ID); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id":       req.ID,
		"released": true,
	})
}

func seatRowFromLease(l lease.Lease, now time.Time) seatRow {
	row := seatRow{
		ID:            l.ItemID,
		Kind:          l.Kind,
		StorySeat:     l.StorySeat,
		SeatKey:       l.SeatKey,
		Worktree:      l.Worktree,
		Owner:         l.Owner,
		State:         l.State,
		InFlight:      lease.EffectiveInFlight(l, now),
		AcquiredAge:   formatAge(now.Sub(l.AcquiredAt)),
		HeartbeatAge:  formatAge(now.Sub(l.HeartbeatAt)),
		Stale:         !lease.Alive(l, now),
		StopRequested: l.StopRequestedBy,
		StopReason:    l.StopReason,
	}
	if act, elapsed, ok := lease.EffectiveActivity(l, now); ok {
		row.Activity = act.Label
		row.ActivityIndex = act.Index
		row.ActivityTotal = act.Total
		row.ActivityAge = formatAge(elapsed)
		row.ActivityGates, row.ActivityPassed, row.ActivityFailed = act.Gates, act.Passed, act.Failed
	}
	if d, ok := lease.EffectiveActivityDetail(l, now); ok {
		row.Agent = d.Agent
		row.Model = d.Model
		row.Pid = d.Pid
		row.LastEvent = d.EventLabel
		row.LastEventAge = formatAge(now.Sub(d.EventAt))
		row.EventCount = d.EventCount
		row.IdleTimeout = resolveDisplayIdleTimeout(d.Agent)
	}
	return row
}

// resolveDisplayIdleTimeout resolves agent's idle-stall bound the same way the
// engine does (binding idle_timeout=, else [defaults], else the shipped
// default) purely for display on an in-flight seat row. Empty when the agents
// layer is unwired (tests / pre-init) or the binding is unknown — never a
// refusal, since this is read-only observability (sty_752c4ef2 AC7).
func resolveDisplayIdleTimeout(agent string) string {
	if !agentsWired || agent == "" {
		return ""
	}
	b, ok := agentsLayer.NamedBinding(agent)
	if !ok {
		return ""
	}
	idle, err := agentsLayer.ResolveIdleTimeout(b, config.DefaultIdleTimeout)
	if err != nil {
		return ""
	}
	return idle.String()
}

// formatAge renders a duration as a short human age (minutes/hours/days).
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%dh", days, int(d.Hours())%24)
}
