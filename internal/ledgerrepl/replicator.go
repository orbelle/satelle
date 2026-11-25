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

type Objects interface {
	PutIfAbsent(ctx context.Context, key string, body []byte) (existing []byte, created bool, err error)
	Get(ctx context.Context, key string) (body []byte, found bool, err error)
	List(ctx context.Context, prefix, after string) ([]string, error)
}

type Replicator struct {
	Ledger  *ledger.Store
	Store   Store
	Refs    func([]ledger.Event) []ledger.BlobRef
	Out     io.Writer
	Cache   *cache.Cache
	Choose  Chooser
	Mirrors FileMirrors
}

type Chooser interface {
	WorkflowFor(it item.Item) (workflow, start string, err error)
}

type FileMirrors interface {
	MirrorTail(id string, local []ledger.Event, from int) bool
	Remirror(id string) (wrote bool, err error)
}

const Actor = "satelle"

const (
	ChainsPrefix = "chains/"
	BlobsPrefix  = "blobs/"
)

func ChainKey(item string, seq int) string {
	return fmt.Sprintf("%s%s/%012d.json", ChainsPrefix, item, seq)
}

func BlobKey(sha string) string { return BlobsPrefix + sha }

var chainKeyPattern = regexp.MustCompile(`^chains/([A-Za-z0-9][A-Za-z0-9_.-]*)/([0-9]{12})\.json$`)

type Divergence struct {
	Item      string
	Seq       int
	StoreHash string
	LocalHash string
}

type Result struct {
	Events           int
	Blobs            int
	Items            int
	Gaps             []ledger.Gap
	Diverged         []Divergence
	Refused          []string
	Written          []string
	Placed, Unplaced []string
	Adopted          []string
}

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

func (r Result) Failed() bool { return len(r.Diverged) > 0 || len(r.Refused) > 0 }

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
	if h == "" {
		return "(no event hash)"
	}
	return ledger.ShortHash(h)
}

func holeLine(item string, seq int) string {
	return fmt.Sprintf("the store's chain %s has no event at %d; %s left untouched", item, seq, item)
}

func refsBySeq(refs []ledger.BlobRef) map[int][]ledger.BlobRef {
	out := map[int][]ledger.BlobRef{}
	for _, r := range refs {
		out[r.Seq] = append(out[r.Seq], r)
	}
	return out
}
