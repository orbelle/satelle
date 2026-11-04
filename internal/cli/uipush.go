package cli

import (
	"context"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/push"
)

// uiDrainBudget bounds the total wall time added by end-of-invocation push
// delivery (change events + one snapshot). Shared across every request in a
// single flush so a black-holed endpoint cannot hang the verb (sty_9ba3d709).
const uiDrainBudget = 1500 * time.Millisecond

// uiDrain records mutations during a one-shot CLI invocation and delivers them
// synchronously before the store closes. mark is the ChangeNotifier sink (no
// I/O); flush posts distinct topics then one snapshot, fail-silent.
type uiDrain struct {
	mu       sync.Mutex
	endpoint string
	repoKey  string
	app      *app.App
	origin   mirror.IngestOrigin
	topics   []string
	seen     map[string]struct{}
	once     sync.Once
}

// mark records a distinct topic. Safe for concurrent use; never performs I/O.
func (d *uiDrain) mark(topic string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	if _, ok := d.seen[topic]; ok {
		return
	}
	d.seen[topic] = struct{}{}
	d.topics = append(d.topics, topic)
}

// markMirrorDirty records that a's latest state did not reach the mirror, so
// satelled's retry tick reseeds the repo within seconds (mirror.DirtyDir). One
// local file write: no retry and no wait on the verb path. Best-effort.
func markMirrorDirty(a *app.App) {
	if a == nil {
		return
	}
	// GlobalDir panics in a test with no isolated home; a mark is never worth that.
	defer func() { _ = recover() }()
	_ = mirror.MarkDirty(config.GlobalDir(), config.CanonicalRepoRoot(a.RepoRoot))
}

// flush delivers pending change events then one snapshot under a single budget.
// Idempotent (sync.Once). Fail-silent: never panics, never returns an error to
// the verb path; a snapshot that does not land marks the repo dirty. No-op when
// endpoint empty or nothing was marked.
func (d *uiDrain) flush() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		defer func() { _ = recover() }()
		d.mu.Lock()
		topics := append([]string(nil), d.topics...)
		endpoint := d.endpoint
		repoKey := d.repoKey
		a := d.app
		origin := d.origin
		d.mu.Unlock()
		if endpoint == "" || len(topics) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), uiDrainBudget)
		defer cancel()
		pub := &push.Publisher{Endpoint: endpoint, RepoKey: repoKey, Origin: string(origin)}
		for _, t := range topics {
			_ = pub.PostContext(ctx, t)
		}
		// Snapshot last: handleSnapshot rings the all-topics doorbell after
		// bodies land, so the final SSE reload sees full state. Drain uses a
		// light partial snapshot so large repos land inside uiDrainBudget
		// (sty_3562c820); workspace add still sends the full body.
		if a == nil {
			return
		}
		snap, err := buildUIDrainSnapshot(ctx, a)
		if err != nil || snap == nil {
			if err != nil {
				markMirrorDirty(a)
			}
			return
		}
		snap.Origin = origin
		if err := postUISnapshotContext(ctx, endpoint, snap); err != nil {
			markMirrorDirty(a)
		}
	})
}
