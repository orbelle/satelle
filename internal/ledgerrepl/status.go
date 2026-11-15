package ledgerrepl

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bobmcallan/satelle/internal/ledger"
)

const (
	InSync    = "in sync"
	Ahead     = "ahead"
	Behind    = "behind"
	LocalOnly = "local only"
	StoreOnly = "store only"
)

type ItemStatus struct {
	Item  string `json:"item"`
	Local int    `json:"local"`
	Store int    `json:"store"`
	State string `json:"state"`
}

func (r *Replicator) Status(ctx context.Context) ([]ItemStatus, error) {
	lst, err := r.Store.Chains(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, line := range lst.Skipped {
		r.printf("%s\n", line)
	}
	ids, err := r.Ledger.Items()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ItemStatus
	for _, id := range ids {
		local, err := r.Ledger.Read(id)
		if err != nil {
			return nil, err
		}
		if len(local) == 0 {
			continue
		}
		seen[id] = true
		c, ok := lst.Chains[id]
		st := ItemStatus{Item: id, Local: len(local), Store: c.Len}
		switch {
		case !ok || c.Len == 0:
			st.State = LocalOnly
		case c.Hole >= 0:
			st.State = fmt.Sprintf("hole at %d", c.Hole)
		default:
			if st.State, err = r.compare(ctx, id, local, c); err != nil {
				return nil, err
			}
		}
		out = append(out, st)
	}
	for id, c := range lst.Chains {
		if !seen[id] {
			out = append(out, ItemStatus{Item: id, Store: c.Len, State: StoreOnly})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item < out[j].Item })
	return out, nil
}

func (r *Replicator) compare(ctx context.Context, id string, local []ledger.Event, c Chain) (string, error) {
	n := c.Len
	if !proven(local, c) {
		s, err := ledger.SharedPrefix(local, n, func(seq int) (string, error) { return r.Store.HashAt(ctx, id, seq) })
		var ref *refusal
		if errors.As(err, &ref) {
			return "refused: " + ref.line, nil
		}
		if err != nil {
			return "", err
		}
		if s < min(n, len(local)) {
			return fmt.Sprintf("diverged at %d", s), nil
		}
	}
	switch {
	case n == len(local):
		return InSync, nil
	case n < len(local):
		return Ahead, nil
	}
	return Behind, nil
}
