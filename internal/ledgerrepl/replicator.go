// Package ledgerrepl replicates a runtime's ledger to and from a ledger
// store, by the same rules whatever the store: push appends each chain past
// what the store holds, with the blobs the chains reference; pull brings them
// back verified. A chain that diverged from the store's is refused at its
// fork, never merged. A blob that is missing on either side is a gap: named,
// never fatal. The store is a Store: an external store command's adapter
// operations (ObjectStore) or satelle.dev (Dev).
package ledgerrepl

import (
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// Objects is a store's adapter operations, as ledgerstore.Command implements
// them.
type Objects interface {
	PutIfAbsent(ctx context.Context, key string, body []byte) (existing []byte, created bool, err error)
	Get(ctx context.Context, key string) (body []byte, found bool, err error)
	List(ctx context.Context, prefix, after string) ([]string, error)
}

// Replicator carries the chains and blobs of Ledger to and from Store. Refs
// lists the blobs a chain references; Out receives one line per divergence,
// refusal, gap, fill and skipped entry. Cache, when set, is refreshed after
// every pull. Choose, when set, places an item a pull brought in without a
// workflow by this repo's rules. Mirrors, when set, lets a pull replace a
// forked local chain that holds only this repo's configuration-file mirror
// events with the store's chain; nil adopts nothing.
type Replicator struct {
	Ledger  *ledger.Store
	Store   Store
	Refs    func([]ledger.Event) []ledger.BlobRef
	Out     io.Writer
	Cache   *cache.Cache
	Choose  Chooser
	Mirrors FileMirrors
}

// Chooser picks the workflow an item belongs in and its start state (the
// kernel's rules: the item's own, else the first matching rule, else the
// default).
type Chooser interface {
	WorkflowFor(it item.Item) (workflow, start string, err error)
}

// FileMirrors is the repo's knowledge of chains it writes from its own
// configuration files (the kernel's queue files).
type FileMirrors interface {
	// MirrorOnly reports whether every event of local was written from this
	// repo's configuration files, so the store's chain may replace it.
	MirrorOnly(id string, local []ledger.Event) bool
	// Remirror applies the file's definition to the chain now held; wrote
	// reports whether it appended an event.
	Remirror(id string) (wrote bool, err error)
}

// Actor records the events satelle itself appends (placing a pulled item).
const Actor = "satelle"

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
	Items    int // items an event (or, on push, a blob) was written to: len(Written)
	Gaps     []ledger.Gap
	Diverged []Divergence
	Refused  []string // items left untouched for another reason, one line each
	// Written is the items counted in Items, in order.
	Written []string
	// Placed are items a pull brought in without a workflow and placed by
	// this repo's rules; Unplaced could not be (local events unpushed, or
	// the store kept moving).
	Placed, Unplaced []string
	// Adopted are items whose local chain, only file-mirror events, a pull
	// replaced with the store's chain.
	Adopted []string
}

// add folds o into r: counts are summed and lists appended.
func (r *Result) add(o Result) {
	r.Events += o.Events
	r.Blobs += o.Blobs
	r.Items += o.Items
	r.Gaps = append(r.Gaps, o.Gaps...)
	r.Diverged = append(r.Diverged, o.Diverged...)
	r.Refused = append(r.Refused, o.Refused...)
	r.Written = append(r.Written, o.Written...)
	r.Placed = append(r.Placed, o.Placed...)
	r.Unplaced = append(r.Unplaced, o.Unplaced...)
	r.Adopted = append(r.Adopted, o.Adopted...)
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
