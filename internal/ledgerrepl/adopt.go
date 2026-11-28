package ledgerrepl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

type SetAside struct {
	Item   string
	Fork   int
	Path   string
	Events []ledger.Event
	Taken  int
}

func (r *Replicator) Divergent(ctx context.Context) ([]string, error) {
	rows, err := r.Status(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range rows {
		if strings.HasPrefix(row.State, divergedAt) {
			out = append(out, row.Item)
		}
	}
	return out, nil
}

type forkedItem struct {
	item  string
	chain Chain
	local []ledger.Event
	fork  int
}

func (r *Replicator) Adopt(ctx context.Context, items []string) ([]SetAside, Result, error) {
	var res Result
	if len(items) == 0 {
		return nil, res, errors.New("ledger adopt: no item named")
	}
	forks, err := r.forkedItems(ctx, &res, items)
	if err != nil || res.Failed() {
		return nil, res, err
	}
	var done []SetAside
	var adopted []string
	for _, f := range forks {
		s, err := r.adoptFork(ctx, f)
		var ref *refusal
		if errors.As(err, &ref) {
			r.refuse(&res, ref.line)
			continue
		}
		if err != nil {
			return done, res, err
		}
		done = append(done, s)
		adopted = append(adopted, f.item)
		res.Events += s.Taken
		res.Items++
		res.Written = append(res.Written, f.item)
		res.Adopted = append(res.Adopted, f.item)
	}
	if err := r.fillBlobs(ctx, &res, adopted); err != nil {
		return done, res, err
	}
	for _, s := range done {
		r.printf("adopted: %s: this runtime's events %d..%d set aside in %s; the store's %d event(s) taken\n",
			s.Item, s.Fork, s.Fork+len(s.Events)-1, s.Path, s.Taken)
		for i, e := range s.Events {
			r.printf("  %d  %s  %s  %s\n", s.Fork+i, e.Kind, e.Actor, e.At.UTC().Format(time.RFC3339))
		}
	}
	return done, res, nil
}

func (r *Replicator) forkedItems(ctx context.Context, res *Result, items []string) ([]forkedItem, error) {
	lst, err := r.Store.Chains(ctx, items)
	if err != nil {
		return nil, err
	}
	for _, line := range lst.Skipped {
		r.printf("%s\n", line)
	}
	var out []forkedItem
	for _, item := range items {
		c, ok := lst.Chains[item]
		if !ok || c.Len == 0 {
			r.refuse(res, fmt.Sprintf("the store holds no chain %s", item))
			continue
		}
		if c.Hole >= 0 {
			r.refuse(res, holeLine(item, c.Hole))
			continue
		}
		local, err := r.Ledger.Read(item)
		if err != nil {
			return nil, err
		}
		state, fork, err := r.state(ctx, item, local, c)
		if err != nil {
			return nil, err
		}
		if fork < 0 {
			r.refuse(res, fmt.Sprintf("%s is %s: only a chain that diverged from the store's is adopted", item, state))
			continue
		}
		out = append(out, forkedItem{item: item, chain: c, local: local, fork: fork})
	}
	return out, nil
}

func (r *Replicator) adoptFork(ctx context.Context, f forkedItem) (SetAside, error) {
	fetched, err := r.fetchVerified(ctx, f.item, f.chain)
	if err != nil {
		return SetAside{}, err
	}
	if !sharesPrefix(f.local, fetched, f.fork) {
		storeHash := ""
		if len(fetched) >= f.fork {
			storeHash = fetched[f.fork-1].Hash
		}
		return SetAside{}, &refusal{fmt.Sprintf("%s event %d from the store is not the one the fork at %d was measured against (the store holds %s, this runtime %s); nothing replaced",
			f.item, f.fork-1, f.fork, short(storeHash), short(f.local[f.fork-1].Hash))}
	}
	kept := f.local[f.fork:]
	path, err := r.Ledger.SetAside(f.item, f.fork, kept)
	if err != nil {
		return SetAside{}, err
	}
	if err := r.replaceWith(f.item, f.local, fetched, "adopting"); err != nil {
		if rmErr := os.Remove(path); rmErr != nil {
			return SetAside{}, errors.Join(err, rmErr)
		}
		return SetAside{}, err
	}
	return SetAside{Item: f.item, Fork: f.fork, Path: path, Events: kept, Taken: len(fetched)}, nil
}
