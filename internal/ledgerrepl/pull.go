package ledgerrepl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Pull brings the store's chains into this runtime. For each item it finds
// the prefix both chains share, comparing at the last sequence both hold
// whatever their lengths: a fork is refused naming the item and fork point
// and nothing is appended; a store chain that is the local chain or a prefix
// of it needs nothing; a longer one has its new events fetched in order,
// verified with the local chain, then appended. Then every blob a local chain
// references and this runtime lacks is fetched and checked against its sha256
// before it is stored; one the store lacks, or holds with other bytes, is a
// gap. A store error ends the pull and is returned.
func (r *Replicator) Pull(ctx context.Context) (Result, error) {
	var res Result
	lengths, holes, err := r.storeChains(ctx)
	if err != nil {
		return res, err
	}
	items := make([]string, 0, len(lengths))
	for item := range lengths {
		items = append(items, item)
	}
	sort.Strings(items)
	for _, item := range items {
		if seq, ok := holes[item]; ok {
			r.refuse(&res, holeLine(item, seq))
			continue
		}
		err := r.pullItem(ctx, &res, item, lengths[item])
		var ref *refusal
		if errors.As(err, &ref) {
			r.refuse(&res, ref.line)
			continue
		}
		if err != nil {
			return res, err
		}
	}
	return res, r.fillBlobs(ctx, &res)
}

// storeEvent gets and decodes the store's event at seq of item.
func (r *Replicator) storeEvent(ctx context.Context, item string, seq int) (ledger.Event, error) {
	key := ChainKey(item, seq)
	body, found, err := r.Objects.Get(ctx, key)
	if err != nil {
		return ledger.Event{}, err
	}
	if !found {
		return ledger.Event{}, &refusal{fmt.Sprintf("the store listed %s but has no such key; %s left untouched", key, item)}
	}
	var e ledger.Event
	if err := json.Unmarshal(body, &e); err != nil {
		return e, &refusal{fmt.Sprintf("%s event %d from the store is not an event (%v); nothing appended", item, seq, err)}
	}
	if e.Item != item {
		return e, &refusal{fmt.Sprintf("%s event %d from the store names item %q; nothing appended", item, seq, e.Item)}
	}
	return e, nil
}

func (r *Replicator) pullItem(ctx context.Context, res *Result, item string, n int) error {
	local, err := r.Ledger.Read(item)
	if err != nil {
		return err
	}
	seen := map[int]string{}
	hashAt := func(seq int) (string, error) {
		e, err := r.storeEvent(ctx, item, seq)
		if err != nil {
			return "", err
		}
		seen[seq] = e.Hash
		return e.Hash, nil
	}
	s, err := ledger.SharedPrefix(local, n, hashAt)
	if err != nil {
		return err
	}
	if s < min(n, len(local)) {
		r.diverge(res, Divergence{Item: item, Seq: s, StoreHash: seen[s], LocalHash: local[s].Hash}, "nothing appended")
		return nil
	}
	if n <= len(local) {
		return nil // the store holds this chain or a prefix of it
	}
	chain := append([]ledger.Event(nil), local...)
	for seq := len(local); seq < n; seq++ {
		e, err := r.storeEvent(ctx, item, seq)
		if err != nil {
			return err
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
	}
	return err
}

// fillBlobs fetches each blob a local chain references and this runtime does
// not hold, checking its sha256 before storing it.
func (r *Replicator) fillBlobs(ctx context.Context, res *Result) error {
	if r.Refs == nil {
		return nil
	}
	items, err := r.Ledger.Items()
	if err != nil {
		return err
	}
	unfilled := map[string]string{} // sha -> why the store could not fill it
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
			why, tried := unfilled[sha]
			if !tried {
				if why, err = r.fetchBlob(ctx, sha); err != nil {
					return err
				}
				unfilled[sha] = why
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

// fetchBlob gets sha from the store and stores it when its body hashes to
// sha. It returns why it did not, or "" when the blob is now held.
func (r *Replicator) fetchBlob(ctx context.Context, sha string) (string, error) {
	body, found, err := r.Objects.Get(ctx, BlobKey(sha))
	if err != nil {
		return "", err
	}
	if !found {
		return whyNotInStore, nil
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != sha {
		return fmt.Sprintf("the store's body hashes to %s; not stored", got), nil
	}
	if _, err := r.Ledger.PutBlob(body); err != nil {
		return "", err
	}
	return "", nil
}
