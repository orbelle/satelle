package ledgerrepl

import (
	"context"
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const placeAttempts = 3

func (r *Replicator) place(ctx context.Context, res *Result, id string) error {
	for range placeAttempts {
		local, err := r.Ledger.Read(id)
		if err != nil || len(local) == 0 {
			return err
		}
		it, err := item.Fold(local)
		if err != nil || it.Workflow != "" {
			return nil
		}
		lst, err := r.listOne(ctx, id)
		if err != nil {
			return err
		}
		c := lst.Chains[id]
		if c.Len != len(local) || c.Hole >= 0 {
			res.Unplaced = append(res.Unplaced, id)
			return nil
		}
		h := c.Head
		if h == "" {
			if h, err = r.Store.HashAt(ctx, id, c.Len-1); err != nil {
				return err
			}
		}
		head := local[len(local)-1].Hash
		if h != head {
			res.Unplaced = append(res.Unplaced, id)
			return nil
		}
		name, start, err := r.Choose.WorkflowFor(it)
		if err != nil {
			return fmt.Errorf("place %s: %w", id, err)
		}
		f, m := item.Placement(name, it.Status, start)
		e1, err := r.Ledger.NewEvent(id, item.KindUpdated, Actor, f, head)
		if err != nil {
			return err
		}
		e2, err := r.Ledger.NewEvent(id, item.KindTransitioned, Actor, m, e1.Hash)
		if err != nil {
			return err
		}
		placed := []ledger.Event{e1, e2}
		n, _, err := r.Store.Offer(ctx, id, len(local), placed)
		if n > 0 {
			if _, ierr := r.Ledger.Import(append(append([]ledger.Event(nil), local...), placed[:n]...)); ierr != nil {
				return fmt.Errorf("place %s: %w", id, ierr)
			}
		}
		if err != nil {
			return err
		}
		if n == len(placed) {
			res.Placed = append(res.Placed, id)
			return nil
		}
		lst, err = r.listOne(ctx, id)
		if err != nil {
			return err
		}
		c, ok := lst.Chains[id]
		if !ok {
			break
		}
		if pulled, err := r.pullOne(ctx, res, id, c); err != nil || !pulled {
			return err
		}
	}
	res.Unplaced = append(res.Unplaced, id)
	return nil
}

func (r *Replicator) listOne(ctx context.Context, id string) (Listing, error) {
	lst, err := r.Store.Chains(ctx, []string{id})
	if line, ok := lst.Refused[id]; ok && err == nil {
		return lst, &refusal{line}
	}
	return lst, err
}
