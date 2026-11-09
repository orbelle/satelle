package mirror

import "time"

// Freshness timings live HERE, in the one package serve, web and the CLI all
// import (serve → web → mirror; web can never import serve), so the sync timer
// that confirms a mirror and the badge that reports one nobody confirmed read
// the same numbers (sto_128803b3).
const (
	// SyncPeriod is how long satelle-sync.timer waits after one automatic sync
	// pass ends before it starts the next. Each pass confirms satelled's mirror
	// of every project it visits: marked fresh when unchanged, otherwise sent a
	// full snapshot.
	SyncPeriod = 30 * time.Second
	// SyncPassTimeout bounds one sync pass: the sync service's TimeoutStartSec.
	SyncPassTimeout = 5 * time.Minute
	// StaleAfter is how long a partition may go without a confirmed ingest
	// before a view must say so rather than presenting its frame as current.
	// A project keeps its place in each pass's order, so two confirmations are
	// at most one period plus one pass apart; one more period on top means
	// stale only once a pass that should have confirmed did not, and a slow
	// pass never flaps the badge.
	StaleAfter = SyncPassTimeout + 2*SyncPeriod
)

// LastIngest parses the partition's last confirmed ingest time. ok is false when
// the stored value is absent or unparseable — callers then have no basis to
// claim freshness.
func (p Partition) LastIngest() (time.Time, bool) {
	if p.UpdatedAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, p.UpdatedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Stale reports whether this partition has gone longer than StaleAfter without a
// confirmed ingest: no automatic sync pass (nor any push) has confirmed it. An
// unparseable/absent timestamp counts as stale: an unknown last-ingest time is
// exactly the case the operator must not read as current.
func (p Partition) Stale(now time.Time) bool {
	t, ok := p.LastIngest()
	if !ok {
		return true
	}
	return now.UTC().Sub(t) > StaleAfter
}
