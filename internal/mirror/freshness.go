package mirror

import "time"

const (
	SyncPeriod      = 30 * time.Second
	SyncPassTimeout = 5 * time.Minute
	StaleAfter      = SyncPassTimeout + 2*SyncPeriod
)

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

func (p Partition) Stale(now time.Time) bool {
	t, ok := p.LastIngest()
	if !ok {
		return true
	}
	return now.UTC().Sub(t) > StaleAfter
}
