// Package ledgerrepl replicates a runtime's ledger through a ledger store's
// adapter operations: push writes every chain, one object per event, and the
// blobs the chains reference; pull brings them back verified. A chain that
// diverged from the store's is refused at its fork, never merged. A blob that
// is missing on either side is a gap: named, never fatal.
package ledgerrepl

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// Objects is a store's adapter operations, as ledgerstore.Command implements
// them.
type Objects interface {
	PutIfAbsent(ctx context.Context, key string, body []byte) (existing []byte, created bool, err error)
	Get(ctx context.Context, key string) (body []byte, found bool, err error)
	List(ctx context.Context, prefix, after string) ([]string, error)
}

// Replicator carries the chains and blobs of Ledger to and from Objects. Refs
// lists the blobs a chain references; Out receives one line per divergence,
// gap, fill and skipped key.
type Replicator struct {
	Ledger  *ledger.Store
	Objects Objects
	Refs    func([]ledger.Event) []ledger.BlobRef
	Out     io.Writer
}

// The store's key prefixes.
const (
	ChainsPrefix = "chains/"
	BlobsPrefix  = "blobs/"
)

// ChainKey is the object key of the event at seq (0-based) on item's chain.
// It carries no hash, so a conditional create tests exactly "no event at this
// sequence".
func ChainKey(item string, seq int) string {
	return fmt.Sprintf("%s%s/%012d.json", ChainsPrefix, item, seq)
}

// BlobKey is the object key of a blob.
func BlobKey(sha string) string { return BlobsPrefix + sha }

var chainKeyPattern = regexp.MustCompile(`^chains/([A-Za-z0-9][A-Za-z0-9_.-]*)/([0-9]{12})\.json$`)

// Divergence is an item whose chain and the store's differ at Seq.
type Divergence struct {
	Item      string
	Seq       int
	StoreHash string
	LocalHash string
}

// Result is what one push or pull did.
type Result struct {
	Events   int // events written to the store (push) or appended here (pull)
	Blobs    int // blobs written to the store (push) or filled here (pull)
	Items    int // items an event was written to
	Gaps     []ledger.Gap
	Diverged []Divergence
	Refused  []string // items left untouched for another reason, one line each
}

// Failed reports whether an item diverged or was refused. Gaps never fail.
func (r Result) Failed() bool { return len(r.Diverged) > 0 || len(r.Refused) > 0 }

// DivergedItems is the ids of the diverged items, in order.
func (r Result) DivergedItems() []string {
	out := make([]string, 0, len(r.Diverged))
	for _, d := range r.Diverged {
		out = append(out, d.Item)
	}
	return out
}

func (r *Replicator) printf(format string, args ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, format, args...)
	}
}

func (r *Replicator) gap(res *Result, g ledger.Gap) {
	res.Gaps = append(res.Gaps, g)
	r.printf("%s\n", g)
}

func (r *Replicator) refuse(res *Result, line string) {
	res.Refused = append(res.Refused, line)
	r.printf("%s\n", line)
}

func (r *Replicator) diverge(res *Result, d Divergence, tail string) {
	res.Diverged = append(res.Diverged, d)
	r.printf("diverged: %s at event %d: the store holds %s, this runtime %s; %s\n",
		d.Item, d.Seq, short(d.StoreHash), short(d.LocalHash), tail)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "(no event hash)"
	}
	return h
}

// storeChains lists the store's chains in one call: each item's length, and
// the first missing sequence of an item whose sequences are not 0..n-1. A key
// under chains/ that is not an event key is named and skipped.
func (r *Replicator) storeChains(ctx context.Context) (lengths, holes map[string]int, err error) {
	keys, err := r.Objects.List(ctx, ChainsPrefix, "")
	if err != nil {
		return nil, nil, err
	}
	seqs := map[string][]int{}
	for _, k := range keys {
		m := chainKeyPattern.FindStringSubmatch(k)
		if m == nil || !ledger.ValidItem(m[1]) {
			r.printf("store key %s is not a chain event key; skipped\n", k)
			continue
		}
		n, _ := strconv.Atoi(m[2])
		seqs[m[1]] = append(seqs[m[1]], n)
	}
	lengths, holes = map[string]int{}, map[string]int{}
	for item, s := range seqs {
		sort.Ints(s)
		for i, n := range s {
			if n != i {
				holes[item] = i
				break
			}
		}
		lengths[item] = len(s)
	}
	return lengths, holes, nil
}

func holeLine(item string, seq int) string {
	return fmt.Sprintf("the store's chain %s has no event at %d; %s left untouched", item, seq, item)
}

// refsBySeq groups refs by the sequence of the event that references them.
func refsBySeq(refs []ledger.BlobRef) map[int][]ledger.BlobRef {
	out := map[int][]ledger.BlobRef{}
	for _, r := range refs {
		out[r.Seq] = append(out[r.Seq], r)
	}
	return out
}
