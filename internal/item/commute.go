package item

import (
	"slices"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func Commutes(before []ledger.Event, local, store ledger.Event) (bool, error) {
	switch local.Kind {
	case KindSeatReleased:
		return releaseCommutes(local, store)
	case KindQueueAdded:
		return queueAddCommutes(before, local, store)
	}
	return false, nil
}

func releaseCommutes(local, store ledger.Event) (bool, error) {
	var r SeatRelease
	if err := local.Decode(&r); err != nil {
		return false, err
	}
	if store.Kind != KindSeatBound {
		return true, nil
	}
	var s Seat
	if err := store.Decode(&s); err != nil {
		return false, err
	}
	return s.Session != r.Session, nil
}

func queueAddCommutes(before []ledger.Event, local, store ledger.Event) (bool, error) {
	var l QueueEntry
	if err := local.Decode(&l); err != nil {
		return false, err
	}
	switch store.Kind {
	case KindSeatBound, KindSeatReleased:
		return true, nil
	case KindQueueAdded:
		var s QueueEntry
		if err := store.Decode(&s); err != nil {
			return false, err
		}
		return s.Item != l.Item || s.Cause == l.Cause, nil
	case KindQueueRemoved:
		var s QueueEntry
		if err := store.Decode(&s); err != nil {
			return false, err
		}
		return s.Item != l.Item, nil
	case KindQueueFired:
		var f QueueFire
		if err := store.Decode(&f); err != nil {
			return false, err
		}
		if slices.Contains(f.Kept, l.Item) || slices.Contains(FoldQueue(before).Explicit, l.Item) {
			return false, nil
		}
		q, err := Fold(before)
		if err != nil {
			return false, err
		}
		return q.Inputs == nil, nil
	}
	return false, nil
}
