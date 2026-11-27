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

const (
	whyNotInStore = "not in the store"
)

type refusal struct{ line string }

func (e *refusal) Error() string { return e.line }

func (r *Replicator) absorb(res *Result, err error) error {
	var ref *refusal
	if errors.As(err, &ref) {
		r.refuse(res, ref.line)
		return nil
	}
	return err
}

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
		items = nil
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
		if line, ok := lst.Refused[item]; ok {
			r.refuse(&res, line)
			continue
		}
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
			if err := r.absorb(&res, r.place(ctx, &res, item)); err != nil {
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

func (r *Replicator) pullOne(ctx context.Context, res *Result, item string, c Chain) (ok bool, err error) {
	diverged, refused := len(res.Diverged), len(res.Refused)
	err = r.absorb(res, r.pullItem(ctx, res, item, c))
	return err == nil && len(res.Diverged) == diverged && len(res.Refused) == refused, err
}

func (r *Replicator) pullItem(ctx context.Context, res *Result, item string, c Chain) error {
	local, err := r.Ledger.Read(item)
	if err != nil {
		return err
	}
	n := c.Len
	if proven(local, c) && n <= len(local) {
		return nil
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
		if r.Mirrors != nil && r.Mirrors.MirrorTail(item, local, s) {
			return r.adopt(ctx, res, item, c, local, s)
		}
		r.diverge(res, Divergence{Item: item, Seq: s, StoreHash: seen[s], LocalHash: local[s].Hash}, "nothing appended")
		return nil
	}
	if n <= len(local) {
		return nil
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

func (r *Replicator) adopt(ctx context.Context, res *Result, item string, c Chain, local []ledger.Event, fork int) error {
	fetched, err := r.fetchVerified(ctx, item, c)
	if err != nil {
		return err
	}
	if !sharesPrefix(local, fetched, fork) {
		d := Divergence{Item: item, Seq: fork - 1, LocalHash: local[fork-1].Hash}
		if len(fetched) >= fork {
			d.StoreHash = fetched[fork-1].Hash
		}
		r.diverge(res, d, "nothing appended")
		return nil
	}
	if err := r.replaceWith(item, local, fetched); err != nil {
		return err
	}
	r.printf("adopted: %s: this runtime's %d queue-file event(s) replaced by the store's %d\n", item, len(local)-fork, len(fetched))
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

func sharesPrefix(local, fetched []ledger.Event, fork int) bool {
	return fork == 0 || (len(fetched) >= fork && fetched[fork-1].Hash == local[fork-1].Hash)
}

func (r *Replicator) fetchVerified(ctx context.Context, item string, c Chain) ([]ledger.Event, error) {
	fetched, err := r.Store.Events(ctx, item, 0, c.Len)
	if err != nil {
		return nil, err
	}
	for i, e := range fetched {
		if e.Item != item {
			return nil, &refusal{fmt.Sprintf("%s event %d from the store names item %q; nothing replaced", item, i, e.Item)}
		}
	}
	if err := ledger.VerifyChain(item, fetched); err != nil {
		var b *ledger.BrokenLink
		if errors.As(err, &b) {
			return nil, &refusal{fmt.Sprintf("%s event %d from the store does not verify: %s; nothing replaced", item, b.Index, b.Reason)}
		}
		return nil, &refusal{fmt.Sprintf("%s from the store does not verify: %v; nothing replaced", item, err)}
	}
	return fetched, nil
}

func (r *Replicator) replaceWith(item string, local, fetched []ledger.Event) error {
	if err := r.Ledger.Replace(item, local[len(local)-1].Hash, fetched); err != nil {
		var stale *ledger.StaleTail
		if errors.As(err, &stale) {
			return &refusal{fmt.Sprintf("%s changed in this runtime while adopting the store's chain; nothing replaced", item)}
		}
		return err
	}
	if r.Cache != nil {
		return r.Cache.ResetItem(r.Ledger, item)
	}
	return nil
}

func (r *Replicator) fillBlobs(ctx context.Context, res *Result, items []string) error {
	if r.Refs == nil {
		return nil
	}
	unfilled := map[string]string{}
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
				r.gap(res, g)
				continue
			}
			sha := g.Ref.SHA256
			key := item + "\x00" + g.Ref.Name + "\x00" + sha
			why, tried := unfilled[key]
			if !tried {
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

func (r *Replicator) fetchBlob(ctx context.Context, item string, ref ledger.BlobRef) (string, error) {
	body, found, err := r.Store.GetBlob(ctx, item, ref)
	var refused *refusal
	if errors.As(err, &refused) {
		return refused.line, nil
	}
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
