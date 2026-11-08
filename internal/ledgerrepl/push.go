package ledgerrepl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// The reasons push names a gap.
const (
	whyNotHeld  = "not in this runtime and not in the store"
	whyCorrupt  = "corrupt in this runtime and not in the store"
	whyTooLarge = "larger than the store's 64 MiB object limit"
)

// Push writes every local chain to the store, one object per event by
// conditional create, each referenced blob before the event that references
// it. An event already in the store with the same hash is already there; a
// different hash stops that item's push at that sequence (a divergence) while
// other items continue. A blob neither side holds is a gap. A store error ends
// the push and is returned.
func (r *Replicator) Push(ctx context.Context) (Result, error) {
	var res Result
	lengths, holes, err := r.storeChains(ctx)
	if err != nil {
		return res, err
	}
	held := map[string]bool{}
	keys, err := r.Objects.List(ctx, BlobsPrefix, "")
	if err != nil {
		return res, err
	}
	for _, k := range keys {
		if sha, ok := strings.CutPrefix(k, BlobsPrefix); ok {
			held[sha] = true
		}
	}
	items, err := r.Ledger.Items()
	if err != nil {
		return res, err
	}
	for _, item := range items {
		local, err := r.Ledger.Read(item)
		if err != nil {
			return res, err
		}
		if len(local) == 0 {
			continue
		}
		if seq, ok := holes[item]; ok {
			r.refuse(&res, holeLine(item, seq))
			continue
		}
		if err := r.pushItem(ctx, &res, held, item, local, lengths[item]); err != nil {
			return res, err
		}
	}
	return res, nil
}

// pushItem writes item's chain past what the store holds. It re-offers the
// last sequence both hold, so that sequence is compared by the same rule as a
// race with another writer.
func (r *Replicator) pushItem(ctx context.Context, res *Result, held map[string]bool, item string, local []ledger.Event, n int) error {
	start := max(min(n, len(local))-1, 0)
	var refs map[int][]ledger.BlobRef
	if r.Refs != nil {
		refs = refsBySeq(r.Refs(local))
	}
	wrote := false
	// A blob of an event the store already holds may have reached this
	// runtime late: offer it now.
	for seq := 0; seq < start; seq++ {
		for _, ref := range refs[seq] {
			ok, err := r.pushBlob(ctx, res, held, item, ref)
			if err != nil {
				return err
			}
			wrote = wrote || ok
		}
	}
	for seq := start; seq < len(local); seq++ {
		for _, ref := range refs[seq] {
			ok, err := r.pushBlob(ctx, res, held, item, ref)
			if err != nil {
				return err
			}
			wrote = wrote || ok
		}
		body, err := ledger.Encode(local[seq])
		if err != nil {
			return err
		}
		existing, created, err := r.Objects.PutIfAbsent(ctx, ChainKey(item, seq), body)
		if err != nil {
			return err
		}
		if created {
			res.Events++
			wrote = true
			continue
		}
		var there ledger.Event
		if json.Unmarshal(existing, &there) == nil && there.Hash == local[seq].Hash {
			continue // already there
		}
		r.diverge(res, Divergence{Item: item, Seq: seq, StoreHash: there.Hash, LocalHash: local[seq].Hash}, "not pushed past it")
		break
	}
	if wrote {
		res.Items++
	}
	return nil
}

// pushBlob writes ref's blob unless the store holds it. It reports whether it
// wrote one; a blob this runtime cannot supply is a gap.
func (r *Replicator) pushBlob(ctx context.Context, res *Result, held map[string]bool, item string, ref ledger.BlobRef) (bool, error) {
	if held[ref.SHA256] {
		return false, nil
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
	_, created, err := r.Objects.PutIfAbsent(ctx, BlobKey(ref.SHA256), b)
	if err != nil {
		return false, err
	}
	held[ref.SHA256] = true
	if created {
		res.Blobs++
	}
	return created, nil
}
