package ledgerrepl

import (
	"context"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const undecodable = " (payload does not decode)"

func (r *Replicator) rebase(ctx context.Context, res *Result, c Chain, local []ledger.Event, d Divergence) error {
	fork := d.Seq
	if fork == 0 {
		r.diverge(res, d, "nothing appended")
		return nil
	}
	fetched, err := r.fetchVerified(ctx, d.Item, c)
	if err != nil {
		return err
	}
	if _, err := item.Fold(fetched); err != nil || !sharesPrefix(local, fetched, fork) {
		r.diverge(res, d, "nothing appended")
		return nil
	}
	tail, newer := local[fork:], fetched[fork:]
	for i, l := range tail {
		for j, s := range newer {
			ok, err := item.Commutes(fetched[:fork+j], l, s)
			if err == nil && ok {
				continue
			}
			d.Conflict = conflict(l, fork+i, s, fork+j, newer, fork)
			r.diverge(res, d, "")
			return nil
		}
	}
	prev := fetched[len(fetched)-1].Hash
	chain := append([]ledger.Event(nil), fetched...)
	for _, e := range tail {
		ne, err := ledger.Relink(e, prev)
		if err != nil {
			return err
		}
		prev = ne.Hash
		chain = append(chain, ne)
	}
	if err := r.replaceWith(d.Item, local, chain, "re-basing onto"); err != nil {
		return err
	}
	r.printf("rebased: %s: this runtime's %d event(s) from %d re-based onto the store's head at %d\n", d.Item, len(tail), fork, c.Len)
	res.Events += len(newer) + len(tail)
	res.Items++
	res.Written = append(res.Written, d.Item)
	res.Rebased = append(res.Rebased, d.Item)
	return nil
}

func conflict(l ledger.Event, lseq int, s ledger.Event, sseq int, newer []ledger.Event, fork int) *Conflict {
	if l.Kind == item.KindTransitioned {
		for k := len(newer) - 1; k >= 0; k-- {
			if newer[k].Kind == item.KindTransitioned {
				s, sseq = newer[k], fork+k
				break
			}
		}
	}
	c := &Conflict{LocalSeq: lseq, StoreSeq: sseq}
	c.LocalKind, c.Local = moveOf(l)
	c.StoreKind, c.Store = moveOf(s)
	return c
}

func moveOf(e ledger.Event) (string, *item.Move) {
	if e.Kind != item.KindTransitioned {
		return e.Kind, nil
	}
	var m item.Move
	if err := e.Decode(&m); err != nil {
		return e.Kind + undecodable, nil
	}
	return e.Kind, &m
}
