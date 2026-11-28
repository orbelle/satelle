package ledgerrepl

import (
	"context"
	"slices"
	"strings"
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
	var forked []ItemStatus
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
			switch {
			case ok:
				pull = append(pull, st.Item)
			case strings.HasPrefix(st.State, divergedAt) && st.Fork > 0:
				pull = append(pull, st.Item)
				forked = append(forked, st)
			default:
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
	res.Unsynced = append(res.Unsynced, unrebased(forked, res.Pull)...)
	if again := distinct(append(slices.Clone(res.Pull.Adopted), res.Pull.Rebased...)); len(again) > 0 {
		pushed, err := r.Push(ctx, again...)
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

func unrebased(forked []ItemStatus, pulled Result) []ItemStatus {
	var out []ItemStatus
	for _, st := range forked {
		if slices.Contains(pulled.Rebased, st.Item) || slices.Contains(pulled.Adopted, st.Item) {
			continue
		}
		for _, d := range pulled.Diverged {
			if d.Item == st.Item {
				st.State = d.State()
			}
		}
		out = append(out, st)
	}
	return out
}

func distinct(items []string) []string {
	slices.Sort(items)
	return slices.Compact(items)
}
