package ledgerrepl

import "context"

// SyncResult is what one Sync pass did: the push of the items this runtime
// was ahead on, the pull of the items the store was ahead on, and the items
// the pass left alone because they diverged, hold a hole or were refused.
type SyncResult struct {
	Push, Pull Result
	Unsynced   []ItemStatus
}

// Failed reports whether the push or pull failed or an item was left
// unsynced.
func (r SyncResult) Failed() bool {
	return r.Push.Failed() || r.Pull.Failed() || len(r.Unsynced) > 0
}

// Sync is one automatic pass: one Status listing, then a Push of the items
// this runtime is ahead on (or holds alone) and a Pull of the items the store
// is ahead on (or holds alone), push first. An item in sync is not touched,
// so an idle pass costs the listing and local reads. An item that diverged,
// holds a hole or is refused is written to neither side and is returned in
// Unsynced, the same rule push and pull follow: refused, never merged. A store
// error ends the pass and is returned.
func (r *Replicator) Sync(ctx context.Context) (SyncResult, error) {
	var res SyncResult
	sts, err := r.Status(ctx)
	if err != nil {
		return res, err
	}
	var push, pull []string
	for _, st := range sts {
		switch st.State {
		case InSync:
		case Ahead, LocalOnly:
			push = append(push, st.Item)
		case Behind, StoreOnly:
			pull = append(pull, st.Item)
		default:
			res.Unsynced = append(res.Unsynced, st)
		}
	}
	// Push and Pull with no items replicate every chain: call them only with
	// a non-empty list.
	if len(push) > 0 {
		if res.Push, err = r.Push(ctx, push...); err != nil {
			return res, err
		}
	}
	if len(pull) > 0 {
		if res.Pull, err = r.Pull(ctx, pull...); err != nil {
			return res, err
		}
	}
	return res, nil
}
