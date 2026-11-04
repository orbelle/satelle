package throughput

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
	"github.com/bobmcallan/satelle/internal/workspace"
)

// Repo is what ReadRepo reads from one repo: the items that finished in the
// window, and the evidence work that came back is counted from.
type Repo struct {
	Items   []Item
	Returns Returns
}

// qualityRow reports whether a row of kind is one the quality figures read.
func qualityRow(kind string) bool {
	switch kind {
	case ledger.KindReviewAccept, ledger.KindReviewReject, ledger.KindPerformerReject,
		ledger.KindReadinessAccept, ledger.KindAgentInvocation:
		return true
	}
	return false
}

// Options is what the report reads besides the repos and the window.
type Options struct {
	// Pricing prices each item's measured tokens at read time
	// (costview.WithPricing); the zero value reads every cost unavailable.
	Pricing costview.Pricing
}

// ReadRepo returns the stories and epics of the repo at root that finished in w,
// and the stories created in w with the done times of the stories they cite,
// reading it read-only: its ledger through workspace.ReadLedgerReadOnly, its
// stories and routes through workspace.LoadRepoReadOnly. An item is found by its
// status_transition rows inside w, so one created long before the window and
// finished inside it is never dropped by a page bound. Each finished item's
// whole chain is folded by costview.Own, so its cost covers the whole story,
// not only the rows dated inside w.
func ReadRepo(ctx context.Context, root string, w Window, opt Options) (Repo, error) {
	var finished, created []string
	createdAt := map[string]time.Time{}
	chains := map[string][]ledger.Entry{}
	rows := map[string][]ledger.Entry{}
	whole := map[string][]ledger.Entry{}
	err := workspace.ReadLedgerReadOnly(ctx, root, func(ctx context.Context, ls *ledger.Store) error {
		seen := map[string]bool{}
		if err := ls.ForEachKind(ctx, "", ledger.KindStatusTransition, func(e ledger.Entry) error {
			if e.StoryID != "" && !seen[e.StoryID] && w.Contains(e.CreatedAt) {
				seen[e.StoryID] = true
				finished = append(finished, e.StoryID)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := ls.ForEachKind(ctx, "", ledger.KindStoryCreated, func(e ledger.Entry) error {
			if _, dup := createdAt[e.StoryID]; e.StoryID != "" && !dup && w.Contains(e.CreatedAt) {
				createdAt[e.StoryID] = e.CreatedAt
				created = append(created, e.StoryID)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range finished {
			es, err := ls.ListByStory(ctx, id, "")
			if err != nil {
				return fmt.Errorf("read ledger of %s: %w", id, err)
			}
			for _, e := range es {
				switch {
				case e.Kind == ledger.KindStoryCreated || e.Kind == ledger.KindStatusTransition:
					chains[id] = append(chains[id], e)
				case qualityRow(e.Kind):
					rows[id] = append(rows[id], e)
				}
			}
			whole[id] = es
		}
		return nil
	})
	if err != nil || len(finished)+len(created) == 0 {
		return Repo{}, err
	}
	rv, err := workspace.LoadRepoReadOnly(ctx, root, append(append([]string(nil), finished...), created...)...)
	if err != nil {
		return Repo{}, err
	}
	var out Repo
	done := map[string]bool{}
	for _, it := range rv.Stories {
		if it.Kind != workitem.KindStory || done[it.ID] || it.Category == workitem.EmptyCategory {
			continue
		}
		done[it.ID] = true
		if at, ok := createdAt[it.ID]; ok {
			out.Returns.Created = append(out.Returns.Created, Created{ID: it.ID, At: at, Cites: Cites(it)})
		}
		es, ok := chains[it.ID]
		if !ok {
			continue
		}
		// A story no route resolves for is judged on the store's defaults (Cycle).
		spec, _, _, _ := wfgovern.SpecFor(rv.Workflows, it)
		if fi, ok := Finish(it, es, spec); ok && w.Contains(fi.Finished) {
			fi.Rows = rows[it.ID]
			clk, _ := wfgovern.ClockFor(rv.Workflows, it)
			fi.Own = costview.Own(it, whole[it.ID], clk, w.Until, costview.WithPricing(opt.Pricing))
			out.Items = append(out.Items, fi)
		}
	}
	out.Returns.Done, err = DoneTimes(ctx, root, out.Returns.cited(nil))
	return out, err
}

// DoneTimes is when each of ids that the repo at root holds last reached a
// terminal state of its route (the End of its Cycle). An id the repo does not
// hold, or one not finished, is absent.
func DoneTimes(ctx context.Context, root string, ids []string) (map[string]time.Time, error) {
	done := map[string]time.Time{}
	if len(ids) == 0 {
		return done, nil
	}
	chains := map[string][]ledger.Entry{}
	err := workspace.ReadLedgerReadOnly(ctx, root, func(ctx context.Context, ls *ledger.Store) error {
		for _, id := range ids {
			es, err := ls.ListByStory(ctx, id, ledger.KindStatusTransition)
			if err != nil {
				return fmt.Errorf("read ledger of %s: %w", id, err)
			}
			if len(es) > 0 {
				chains[id] = es
			}
		}
		return nil
	})
	if err != nil || len(chains) == 0 {
		return done, err
	}
	keys := make([]string, 0, len(chains))
	for id := range chains {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	rv, err := workspace.LoadRepoReadOnly(ctx, root, keys...)
	if err != nil {
		return done, err
	}
	for _, it := range rv.Stories {
		es, ok := chains[it.ID]
		if !ok {
			continue
		}
		spec, _, _, _ := wfgovern.SpecFor(rv.Workflows, it)
		if p, ok := Cycle(es, spec); ok {
			done[it.ID] = p.End
		}
	}
	return done, nil
}

// Build reads every root and assembles the report: one section per repo, and a
// total over every repo that could be read. A repo that cannot be read is
// reported with its error and named in the total's Excluded. Work that came
// back in one repo citing a story done in another counts in the total only.
func Build(ctx context.Context, roots []string, w Window, opt Options) Report {
	rep := Report{Window: w, Repos: []RepoReport{}}
	var all []Item
	var excluded, readable []string
	total := Returns{Done: map[string]time.Time{}}
	for _, root := range roots {
		name := config.RepoName(root)
		repo, err := ReadRepo(ctx, root, w, opt)
		if err != nil {
			r := Unavailable(name, root, "repo unreadable: "+err.Error(), w)
			r.Error = err.Error()
			rep.Repos = append(rep.Repos, r)
			excluded = append(excluded, name)
			continue
		}
		for i := range repo.Items {
			repo.Items[i].Repo = name
		}
		readable = append(readable, root)
		all = append(all, repo.Items...)
		total.Created = append(total.Created, repo.Returns.Created...)
		maps.Copy(total.Done, repo.Returns.Done)
		rep.Repos = append(rep.Repos, Summarise(name, root, repo.Items, w, repo.Returns))
	}
	if len(excluded) == len(roots) {
		rep.Total = Unavailable("total", "", ReasonNoRepo, w)
		rep.Total.Excluded = excluded
		return rep
	}
	var crossErr error
	if open := total.cited(total.Done); len(open) > 0 {
		for _, root := range readable {
			done, err := DoneTimes(ctx, root, open)
			if err != nil {
				crossErr = fmt.Errorf("%s: %w", config.RepoName(root), err)
				break
			}
			maps.Copy(total.Done, done)
		}
	}
	rep.Total = Summarise("total", "", all, w, total)
	if crossErr != nil {
		rep.Total.Quality.CameBack = CameBack{Count: Count{Reason: "repo unreadable: " + crossErr.Error()}}
	}
	rep.Total.Excluded = excluded
	return rep
}
