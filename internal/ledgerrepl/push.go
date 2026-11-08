package ledgerrepl

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// The reasons push names a gap.
const (
	whyNotHeld  = "not in this runtime and not in the store"
	whyCorrupt  = "corrupt in this runtime and not in the store"
	whyTooLarge = "larger than the store's 64 MiB object limit"
)

// Push appends each local chain past what the store holds, every blob the
// appended events reference before them: all chains with no items, else only
// the named ones (each must have a local chain). An event already in the
// store with the same hash is already there; a different hash stops that
// item's push at that sequence (a divergence) while other items continue. A
// blob neither side holds is a gap. A store error ends the push and is
// returned. A store that keeps item summaries gets those of the written
// items.
func (r *Replicator) Push(ctx context.Context, items ...string) (Result, error) {
	var res Result
	var lst Listing
	var err error
	ids := items
	if len(items) == 0 {
		if ids, err = r.Ledger.Items(); err != nil {
			return res, err
		}
		lst, err = r.Store.Chains(ctx, nil)
	} else {
		for _, id := range items {
			local, err := r.Ledger.Read(id)
			if err != nil {
				return res, err
			}
			if len(local) == 0 {
				return res, fmt.Errorf("no chain %s in this runtime's ledger", id)
			}
		}
		lst, err = r.Store.Chains(ctx, items)
	}
	if err != nil {
		return res, err
	}
	for _, line := range lst.Skipped {
		r.printf("%s\n", line)
	}
	for _, item := range ids {
		local, err := r.Ledger.Read(item)
		if err != nil {
			return res, err
		}
		if len(local) == 0 {
			continue
		}
		c, ok := lst.Chains[item]
		if ok && c.Hole >= 0 {
			r.refuse(&res, holeLine(item, c.Hole))
			continue
		}
		if err := r.pushItem(ctx, &res, item, local, c); err != nil {
			return res, err
		}
	}
	if s, ok := r.Store.(Summaries); ok {
		if err := s.PutSummaries(ctx, res.Written); err != nil {
			return res, err
		}
	}
	return res, nil
}

// pushItem appends item's chain past what the store holds. When the listing
// proves the store holds exactly a prefix of it, the offer starts after that
// prefix; otherwise it re-offers the last sequence both hold, so that
// sequence is compared by the same rule as a race with another writer.
func (r *Replicator) pushItem(ctx context.Context, res *Result, item string, local []ledger.Event, c Chain) error {
	start := max(min(c.Len, len(local))-1, 0)
	if proven(local, c) {
		start = c.Len
	}
	var refs map[int][]ledger.BlobRef
	if r.Refs != nil {
		refs = refsBySeq(r.Refs(local))
	}
	wrote := false
	// A blob of an event the store already holds may have reached this
	// runtime late: offer it now. Then every blob the offered events
	// reference, before the first of them.
	for seq := range local {
		for _, ref := range refs[seq] {
			ok, err := r.pushBlob(ctx, res, item, ref)
			if err != nil {
				return err
			}
			wrote = wrote || ok
		}
	}
	// The offer loop never offers past len(local), and every pass either
	// advances seq or ends: a store chain that extends the local one answers
	// the re-offered last sequence with the same hash, seq reaches
	// len(local), and nothing is written.
	seq := start
	for seq < len(local) {
		n, held, err := r.Store.Offer(ctx, item, seq, local[seq:])
		seq += n
		res.Events += n
		wrote = wrote || n > 0
		if err != nil {
			return err
		}
		if seq >= len(local) {
			break // everything offered is in the store
		}
		if held != "" && held == local[seq].Hash {
			seq++ // already there
			continue
		}
		r.diverge(res, Divergence{Item: item, Seq: seq, StoreHash: held, LocalHash: local[seq].Hash}, "not pushed past it")
		break
	}
	if wrote {
		res.Items++
		res.Written = append(res.Written, item)
	}
	return nil
}

// pushBlob writes ref's blob unless the store holds it. It reports whether it
// wrote one; a blob this runtime cannot supply is a gap.
func (r *Replicator) pushBlob(ctx context.Context, res *Result, item string, ref ledger.BlobRef) (bool, error) {
	held, err := r.Store.HasBlob(ctx, item, ref)
	if err != nil || held {
		return false, err
	}
	b, err := r.Ledger.GetBlob(ref.SHA256)
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.gap(res, ledger.Gap{Item: item, Ref: ref, Why: whyNotHeld})
		return false, nil
	case errors.Is(err, ledger.ErrCorruptBlob):
		r.gap(res, ledger.Gap{Item: item, Ref: ref, Why: whyCorrupt})
		return false, nil
	case err != nil:
		return false, err
	}
	if ledgerstore.CheckBody(len(b)) != nil {
		r.gap(res, ledger.Gap{Item: item, Ref: ref, Why: whyTooLarge})
		return false, nil
	}
	created, err := r.Store.PutBlob(ctx, item, ref, b)
	if err != nil {
		return false, err
	}
	if created {
		res.Blobs++
	}
	return created, nil
}
