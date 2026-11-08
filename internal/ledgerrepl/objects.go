package ledgerrepl

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// ObjectStore is a Store over an external store command's adapter
// operations: one object per event at chains/<item>/<seq>.json and one per
// blob at blobs/<sha256>. The listing carries no hash, so the Replicator
// re-offers the last sequence both hold and compares it there.
type ObjectStore struct {
	Objects Objects
	// Name is the command's path, for Describe.
	Name string

	blobs map[string]bool // the blobs/ listing, taken once per run
}

// Describe names the store command.
func (o *ObjectStore) Describe() string { return "command " + o.Name }

// Chains lists chains/ (every chain) or chains/<item>/ per named item.
func (o *ObjectStore) Chains(ctx context.Context, items []string) (Listing, error) {
	if items == nil {
		keys, err := o.Objects.List(ctx, ChainsPrefix, "")
		if err != nil {
			return Listing{}, err
		}
		return chainListing(keys), nil
	}
	var keys []string
	for _, item := range items {
		k, err := o.Objects.List(ctx, ChainsPrefix+item+"/", "")
		if err != nil {
			return Listing{}, err
		}
		keys = append(keys, k...)
	}
	return chainListing(keys), nil
}

// chainListing gives each item's length and first missing sequence from its
// event keys; a key that is not an event key is named and skipped.
func chainListing(keys []string) Listing {
	l := Listing{Chains: map[string]Chain{}}
	seqs := map[string][]int{}
	for _, k := range keys {
		m := chainKeyPattern.FindStringSubmatch(k)
		if m == nil || !ledger.ValidItem(m[1]) {
			l.Skipped = append(l.Skipped, fmt.Sprintf("store key %s is not a chain event key; skipped", k))
			continue
		}
		n, _ := strconv.Atoi(m[2])
		seqs[m[1]] = append(seqs[m[1]], n)
	}
	for item, s := range seqs {
		sort.Ints(s)
		c := Chain{Len: len(s), Hole: -1}
		for i, n := range s {
			if n != i {
				c.Hole = i
				break
			}
		}
		l.Chains[item] = c
	}
	return l
}

// event gets and decodes the store's event at seq of item.
func (o *ObjectStore) event(ctx context.Context, item string, seq int) (ledger.Event, error) {
	key := ChainKey(item, seq)
	body, found, err := o.Objects.Get(ctx, key)
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

// HashAt is the hash of the event object at seq.
func (o *ObjectStore) HashAt(ctx context.Context, item string, seq int) (string, error) {
	e, err := o.event(ctx, item, seq)
	return e.Hash, err
}

// Events gets the event objects from..to-1.
func (o *ObjectStore) Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error) {
	var out []ledger.Event
	for seq := from; seq < to; seq++ {
		e, err := o.event(ctx, item, seq)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Offer creates each event by put-if-absent, stopping at the first sequence
// the store holds (exit 3) with the hash of the body it returned.
func (o *ObjectStore) Offer(ctx context.Context, item string, seq int, events []ledger.Event) (int, string, error) {
	for i, e := range events {
		body, err := ledger.Encode(e)
		if err != nil {
			return i, "", err
		}
		existing, created, err := o.Objects.PutIfAbsent(ctx, ChainKey(item, seq+i), body)
		if err != nil {
			return i, "", err
		}
		if !created {
			var there ledger.Event
			if json.Unmarshal(existing, &there) != nil {
				return i, "", nil
			}
			return i, there.Hash, nil
		}
	}
	return len(events), "", nil
}

// HasBlob looks the sha up in one blobs/ listing per run; blobs are
// content-addressed, so the item does not matter.
func (o *ObjectStore) HasBlob(ctx context.Context, _ string, ref ledger.BlobRef) (bool, error) {
	if o.blobs == nil {
		keys, err := o.Objects.List(ctx, BlobsPrefix, "")
		if err != nil {
			return false, err
		}
		o.blobs = map[string]bool{}
		for _, k := range keys {
			if sha, ok := strings.CutPrefix(k, BlobsPrefix); ok {
				o.blobs[sha] = true
			}
		}
	}
	return o.blobs[ref.SHA256], nil
}

// PutBlob is put-if-absent at blobs/<sha256>; already held is success.
func (o *ObjectStore) PutBlob(ctx context.Context, _ string, ref ledger.BlobRef, body []byte) (bool, error) {
	_, created, err := o.Objects.PutIfAbsent(ctx, BlobKey(ref.SHA256), body)
	if err != nil {
		return false, err
	}
	if o.blobs != nil {
		o.blobs[ref.SHA256] = true
	}
	return created, nil
}

// GetBlob gets blobs/<sha256>.
func (o *ObjectStore) GetBlob(ctx context.Context, _ string, ref ledger.BlobRef) ([]byte, bool, error) {
	return o.Objects.Get(ctx, BlobKey(ref.SHA256))
}
