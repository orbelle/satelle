package ledgerrepl

import (
	"context"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// Store is a ledger store as the Replicator sees it: chains by item and the
// blobs they reference. A backend only reports what the store holds and
// creates what it is offered; whether a held event equals a local one is
// always the Replicator's decision. Two backends implement it: ObjectStore
// (an external store command's adapter operations) and Dev (satelle.dev over
// the Chain and Attachment services).
type Store interface {
	// Describe names the store for the operator ("command <path>").
	Describe() string
	// Chains lists every chain the store holds (items nil) or only the
	// named ones; a named item the store lacks is absent.
	Chains(ctx context.Context, items []string) (Listing, error)
	// HashAt is the hash of the store's event at seq (0-based) of item.
	HashAt(ctx context.Context, item string, seq int) (string, error)
	// Events is the store's events from..to-1 of item, in order.
	Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error)
	// Offer is the conditional append: it creates events at seq, seq+1, …
	// in order and stops at the first sequence the store already holds.
	// When created < len(events), held is the hash the store holds at
	// seq+created ("" when that object is not an event). An atomic backend
	// creates all of them or none.
	Offer(ctx context.Context, item string, seq int, events []ledger.Event) (created int, held string, err error)
	// HasBlob reports whether the store holds ref's blob for item.
	HasBlob(ctx context.Context, item string, ref ledger.BlobRef) (bool, error)
	// PutBlob stores ref's body for item; created is false when the store
	// already held it.
	PutBlob(ctx context.Context, item string, ref ledger.BlobRef, body []byte) (created bool, err error)
	// GetBlob is ref's body for item; found is false when the store lacks it.
	GetBlob(ctx context.Context, item string, ref ledger.BlobRef) (body []byte, found bool, err error)
}

// Summaries is a store that also keeps item summaries (satelle.dev's item
// list): PutSummaries stores the current summary of each item.
type Summaries interface {
	PutSummaries(ctx context.Context, ids []string) error
}

// Listing is a store's chains. Skipped holds one line per store entry that
// is not a chain.
type Listing struct {
	Chains  map[string]Chain
	Skipped []string
}

// Chain is one chain as a store lists it: its length, its head hash ("" when
// the listing carries no hash) and the first missing sequence (-1 when the
// sequences have no gap).
type Chain struct {
	Len  int
	Head string
	Hole int
}

// proven reports whether the store's chain c is exactly local[:c.Len]: an
// empty chain, or a listed head equal to local's event at that position
// (each hash covers its prev, so equal heads mean equal chains).
func proven(local []ledger.Event, c Chain) bool {
	return c.Len == 0 || (c.Head != "" && c.Len <= len(local) && local[c.Len-1].Hash == c.Head)
}
