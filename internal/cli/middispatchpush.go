// Mid-dispatch observability push (sty_752c4ef2 AC5): the engagement lease's
// activity record already carries in-flight dispatch metadata (agent, model,
// pid, last event), refreshed throttled by agentstep on every real event. This
// file extends that same refresh to also push a light seat-only snapshot to
// the local serve mirror WHILE the dispatch runs, not only at command end —
// so the web story row's running indicator (AC6) and `satelle story seat`
// (AC7) both stay fresh during a long-running dispatch.
package cli

import (
	"context"
	"time"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// midDispatchPushBudget bounds one mid-dispatch seat push so a black-holed
// local serve endpoint cannot stall the dispatch's event loop for more than a
// moment. Shorter than uiDrainBudget: this may fire many times over one long
// dispatch, not once at command end.
var midDispatchPushBudget = 800 * time.Millisecond

// buildSeatSnapshot is the lightest possible partition push: just the seat
// rows, replacing ONLY the mirror's "seat" partition (Kinds) — a mid-dispatch
// push never touches story/task/doc/ledger state.
func buildSeatSnapshot(ctx context.Context, a *app.App) (*mirror.Snapshot, error) {
	seats, err := listSeatsJSON(ctx, a)
	if err != nil {
		return nil, err
	}
	return &mirror.Snapshot{
		RepoKey: config.RepoKey(a.RepoRoot), Slug: partitionSlug(a),
		Seats: seats, Kinds: []string{"seat"},
	}, nil
}

// pushSeatSnapshot posts a seat-only snapshot to endpoint, best-effort, under
// midDispatchPushBudget. a/endpoint nil-checked so an unwired mirror (no
// `satelle serve`, or no global config) is silently inert. A push that does not
// land marks the repo dirty, so a dispatch start or end still reaches the page
// on satelled's retry tick.
func pushSeatSnapshot(a *app.App, endpoint string) {
	if a == nil || endpoint == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), midDispatchPushBudget)
	defer cancel()
	snap, err := buildSeatSnapshot(ctx, a)
	if err != nil || snap == nil {
		if err != nil {
			markMirrorDirty(a)
		}
		return
	}
	if err := postUISnapshotContext(ctx, endpoint, snap); err != nil {
		markMirrorDirty(a)
	}
}

// activityDetailSink is the SetActivityDetail callback body, extracted so it
// is directly testable without booting the full CLI app (NewApp's PreRun
// wires it as a closure over leases/a/endpoint). Persists the in-flight
// dispatch metadata onto the lease row, then pushes it to the local mirror —
// both best-effort; observability must never fail a dispatch.
func activityDetailSink(leases *lease.Store, a *app.App, endpoint, itemID string, d agentstep.ActivityDetail) {
	if leases == nil || itemID == "" {
		return
	}
	_ = leases.SetActivityDetail(context.Background(), itemID, lease.ActivityDetail{
		Agent: d.Agent, Model: d.Model, Pid: d.Pid,
		EventLabel: d.EventLabel, EventAt: d.EventAt, EventCount: d.EventCount,
	})
	pushSeatSnapshot(a, endpoint)
}
