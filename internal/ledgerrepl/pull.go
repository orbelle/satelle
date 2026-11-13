package ledgerrepl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// The reasons pull names a gap.
const (
	whyNotInStore = "not in the store"
)

// refusal is a store object pull will not take: the item is left untouched.
type refusal struct{ line string }

func (e *refusal) Error() string { return e.line }

// Pull brings the store's chains into this runtime: every chain with no
// items, else only the named ones (one the store lacks is refused). For each
// item it finds the prefix both chains share, comparing at the last sequence
// both hold whatever their lengths: a fork is refused naming the item and
// fork point and nothing is appended, unless Mirrors shows the local chain
// holds only file-mirror events: then the store's chain replaces it and the
// file is mirrored onto it (adopted); a store chain that is the local chain
// or a prefix of it needs nothing and is no conflict; a longer one has its
// new events fetched in order, verified with the local chain, then appended.
// An item that arrived without a workflow is placed when Choose is set. Then
// every blob a pulled chain references and this runtime lacks is fetched and
// checked against its sha256 before it is stored; one the store lacks, or
// holds with other bytes, is a gap. The cache, when set, is refreshed even
// after an error. A store error ends the pull and is returned.
func (r *Replicator) Pull(ctx context.Context, items ...string) (Result, error) {
	res, err := r.pull(ctx, items)
	if r.Cache != nil {
		if serr := r.Cache.Sync(r.Ledger); err == nil {
			err = serr
		}
	}
	if err != nil {
		return res, err
	}
	if s, ok := r.Store.(Summaries); ok {
		err = s.PutSummaries(ctx, res.Placed)
	}
	return res, err
}

func (r *Replicator) pull(ctx context.Context, items []string) (Result, error) {
	var res Result
	if len(items) == 0 {
		items = nil // every chain
	}
	lst, err := r.Store.Chains(ctx, items)
	if err != nil {
		return res, err
	}
	for _, line := range lst.Skipped {
		r.printf("%s\n", line)
	}
	ids := items
	if len(items) == 0 {
		ids = make([]string, 0, len(lst.Chains))
		for item := range lst.Chains {
			ids = append(ids, item)
		}
		sort.Strings(ids)
	}
	for _, item := range ids {
		c, ok := lst.Chains[item]
		if !ok {
			r.refuse(&res, fmt.Sprintf("the store holds no chain %s", item))
			continue
		}
		if c.Hole >= 0 {
			r.refuse(&res, holeLine(item, c.Hole))
			continue
		}
		pulled, err := r.pullOne(ctx, &res, item, c)
		if err != nil {
			return res, err
		}
		if pulled && r.Choose != nil {
			if err := r.place(ctx, &res, item); err != nil {
				return res, err
			}
		}
	}
	scope := items
	if len(items) == 0 {
		if scope, err = r.Ledger.Items(); err != nil {
			return res, err
		}
	}
	return res, r.fillBlobs(ctx, &res, scope)
}

// pullOne pulls one item, printing a refusal; ok is false when the item was
// refused or diverged.
func (r *Replicator) pullOne(ctx context.Context, res *Result, item string, c Chain) (ok bool, err error) {
	diverged := len(res.Diverged)
	err = r.pullItem(ctx, res, item, c)
	var ref *refusal
	if errors.As(err, &ref) {
		r.refuse(res, ref.line)
		return false, nil
	}
	return err == nil && len(res.Diverged) == diverged, err
}

func (r *Replicator) pullItem(ctx context.Context, res *Result, item string, c Chain) error {
	local, err := r.Ledger.Read(item)
	if err != nil {
		return err
	}
	n := c.Len
	if proven(local, c) && n <= len(local) {
		return nil // the store holds this chain or a prefix of it
	}
	seen := map[int]string{}
	hashAt := func(seq int) (string, error) {
		h, err := r.Store.HashAt(ctx, item, seq)
		if err != nil {
			return "", err
		}
		seen[seq] = h
		return h, nil
	}
	s, err := ledger.SharedPrefix(local, n, hashAt)
	if err != nil {
		return err
	}
	if s < min(n, len(local)) {
		if r.Mirrors != nil && r.Mirrors.MirrorOnly(item, local) {
			return r.adopt(ctx, res, item, c, local)
		}
		r.diverge(res, Divergence{Item: item, Seq: s, StoreHash: seen[s], LocalHash: local[s].Hash}, "nothing appended")
		return nil
	}
	if n <= len(local) {
		return nil // the store holds this chain or a prefix of it
	}
	fetched, err := r.Store.Events(ctx, item, len(local), n)
	if err != nil {
		return err
	}
	chain := append([]ledger.Event(nil), local...)
	for i, e := range fetched {
		if e.Item != item {
			return &refusal{fmt.Sprintf("%s event %d from the store names item %q; nothing appended", item, len(local)+i, e.Item)}
		}
		chain = append(chain, e)
	}
	if err := ledger.VerifyChain(item, chain); err != nil {
		var b *ledger.BrokenLink
		if errors.As(err, &b) {
			return &refusal{fmt.Sprintf("%s event %d from the store does not verify: %s; nothing appended", item, b.Index, b.Reason)}
		}
		return &refusal{fmt.Sprintf("%s from the store does not verify: %v; nothing appended", item, err)}
	}
	added, err := r.Ledger.Import(chain)
	res.Events += added
	if added > 0 {
		res.Items++
		res.Written = append(res.Written, item)
	}
	return err
}

// adopt replaces a forked local chain that holds only file-mirror events
// with the store's whole chain, verified, under a check that the local tail
// has not moved; then the cache is reset for the item and the file is
// mirrored onto the adopted chain when they differ.
func (r *Replicator) adopt(ctx context.Context, res *Result, item string, c Chain, local []ledger.Event) error {
	fetched, err := r.Store.Events(ctx, item, 0, c.Len)
	if err != nil {
		return err
	}
	for i, e := range fetched {
		if e.Item != item {
			return &refusal{fmt.Sprintf("%s event %d from the store names item %q; nothing replaced", item, i, e.Item)}
		}
	}
	if err := ledger.VerifyChain(item, fetched); err != nil {
		var b *ledger.BrokenLink
		if errors.As(err, &b) {
			return &refusal{fmt.Sprintf("%s event %d from the store does not verify: %s; nothing replaced", item, b.Index, b.Reason)}
		}
		return &refusal{fmt.Sprintf("%s from the store does not verify: %v; nothing replaced", item, err)}
	}
	if err := r.Ledger.Replace(item, local[len(local)-1].Hash, fetched); err != nil {
		var stale *ledger.StaleTail
		if errors.As(err, &stale) {
			return &refusal{fmt.Sprintf("%s changed in this runtime while adopting the store's chain; nothing replaced", item)}
		}
		return err
	}
	if r.Cache != nil {
		if err := r.Cache.ResetItem(r.Ledger, item); err != nil {
			return err
		}
	}
	r.printf("adopted: %s: this runtime's %d queue-file event(s) replaced by the store's %d\n", item, len(local), len(fetched))
	res.Events += len(fetched)
	res.Items++
	res.Written = append(res.Written, item)
	res.Adopted = append(res.Adopted, item)
	wrote, err := r.Mirrors.Remirror(item)
	if err != nil {
		return err
	}
	if wrote {
		r.printf("mirrored: %s: the file's definition as one item.updated\n", item)
	}
	return nil
}

// fillBlobs fetches each blob a chain of items references and this runtime
// does not hold, checking its sha256 before storing it.
func (r *Replicator) fillBlobs(ctx context.Context, res *Result, items []string) error {
	if r.Refs == nil {
		return nil
	}
	unfilled := map[string]string{} // item, name and sha -> why the store could not fill it
	for _, item := range items {
		events, err := r.Ledger.Read(item)
		if err != nil {
			return err
		}
		gaps, err := r.Ledger.BlobGaps(item, r.Refs(events))
		if err != nil {
			return err
		}
		for _, g := range gaps {
			if g.Why != ledger.GapMissing {
				r.gap(res, g) // a corrupt local body is reported, not replaced
				continue
			}
			sha := g.Ref.SHA256
			key := item + "\x00" + g.Ref.Name + "\x00" + sha
			why, tried := unfilled[key]
			if !tried {
				// An earlier fill (another event or item) may have stored it.
				if _, err := r.Ledger.GetBlob(sha); err == nil {
					r.printf("filled: %s event %d blob %s\n", item, g.Ref.Seq, sha)
					continue
				}
				if why, err = r.fetchBlob(ctx, item, g.Ref); err != nil {
					return err
				}
				unfilled[key] = why
			}
			if why != "" {
				g.Why = why
				r.gap(res, g)
				continue
			}
			if !tried {
				res.Blobs++
			}
			r.printf("filled: %s event %d blob %s\n", item, g.Ref.Seq, sha)
		}
	}
	return nil
}

// fetchBlob gets ref's blob from the store and stores it when its body
// hashes to its sha256. It returns why it did not, or "" when the blob is now
// held.
func (r *Replicator) fetchBlob(ctx context.Context, item string, ref ledger.BlobRef) (string, error) {
	body, found, err := r.Store.GetBlob(ctx, item, ref)
	if err != nil {
		return "", err
	}
	if !found {
		return whyNotInStore, nil
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != ref.SHA256 {
		return fmt.Sprintf("the store's body hashes to %s; not stored", got), nil
	}
	if _, err := r.Ledger.PutBlob(body); err != nil {
		return "", err
	}
	return "", nil
}
