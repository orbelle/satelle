package ledgerrepl

import (
	"context"
)

type SyncResult struct {
	Push, Pull Result
	Unsynced   []ItemStatus
}

func (r SyncResult) Failed() bool {
	return r.Push.Failed() || r.Pull.Failed() || len(r.Unsynced) > 0
}

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
			ok, err := r.adoptable(st)
			if err != nil {
				return res, err
			}
			if ok {
				pull = append(pull, st.Item)
			} else {
				res.Unsynced = append(res.Unsynced, st)
			}
		}
	}
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
	if len(res.Pull.Adopted) > 0 {
		pushed, err := r.Push(ctx, res.Pull.Adopted...)
		res.Push.add(pushed)
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func (r *Replicator) adoptable(st ItemStatus) (bool, error) {
	if r.Mirrors == nil || st.Store == 0 || st.Fork < 0 {
		return false, nil
	}
	local, err := r.Ledger.Read(st.Item)
	if err != nil {
		return false, err
	}
	return r.Mirrors.MirrorTail(st.Item, local, st.Fork), nil
}
