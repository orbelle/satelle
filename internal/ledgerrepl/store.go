package ledgerrepl

import (
	"context"

	"github.com/bobmcallan/satelle/internal/ledger"
)

type Store interface {
	Describe() string
	Chains(ctx context.Context, items []string) (Listing, error)
	HashAt(ctx context.Context, item string, seq int) (string, error)
	Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error)
	Offer(ctx context.Context, item string, seq int, events []ledger.Event) (created int, held string, err error)
	HasBlob(ctx context.Context, item string, ref ledger.BlobRef) (bool, error)
	PutBlob(ctx context.Context, item string, ref ledger.BlobRef, body []byte) (created bool, err error)
	GetBlob(ctx context.Context, item string, ref ledger.BlobRef) (body []byte, found bool, err error)
}

type Summaries interface {
	PutSummaries(ctx context.Context, ids []string) error
}

type Listing struct {
	Chains  map[string]Chain
	Skipped []string
	Refused map[string]string
}

type Chain struct {
	Len  int
	Head string
	Hole int
}

func proven(local []ledger.Event, c Chain) bool {
	return c.Len == 0 || (c.Head != "" && c.Len <= len(local) && local[c.Len-1].Hash == c.Head)
}
