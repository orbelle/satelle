package ledgerrepl

import (
	"context"
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// placeAttempts bounds how often a placement retries after another writer
// appended first.
const placeAttempts = 3

// place gives an item that arrived without a workflow (a server creates items
// but never decides process) its workflow by this repo's rules:
// item.updated {workflow} then item.transitioned {from, to: the start state},
// both by Actor. They are offered to the store first, on the head the local
// chain ends at, and imported only once the store took them, so when two
// runtimes race only one placement lands and every runtime agrees. An item
// whose local chain the store does not hold exactly (local events unpushed)
// is left unplaced: placing it would fork.
func (r *Replicator) place(ctx context.Context, res *Result, id string) error {
	for range placeAttempts {
		local, err := r.Ledger.Read(id)
		if err != nil || len(local) == 0 {
			return err
		}
		it, err := item.Fold(local)
		if err != nil || it.Workflow != "" {
			return nil // not an item, or already placed
		}
		lst, err := r.Store.Chains(ctx, []string{id})
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
			res.Unplaced = append(res.Unplaced, id) // diverged: the pull reported it
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
		// Another writer appended first: bring its events in through the
		// one pull path (it verifies the whole chain) and look again.
		lst, err = r.Store.Chains(ctx, []string{id})
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
