package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	Register(&Verb{
		Name:        "story-comparables",
		Description: "Enumerate finished stories with measured token actuals, ranked by similarity to a story (enumeration only)",
		Invoke:      storyComparables,
	})
}

// comparablesReq is the request body for story-comparables. IgnorePrefixes are
// tag prefixes left out of the shared-tag count; the verb has no default list —
// which tags say nothing about comparability is the calling gate's policy.
type comparablesReq struct {
	ID             string   `json:"id"`
	Limit          int      `json:"limit,omitempty"`
	IgnorePrefixes []string `json:"ignore_prefixes,omitempty"`
}

// ComparableActuals are a comparable's measured token figures. A figure the
// story never measured (absent tag, or recordActual's "unavailable") is nil and
// omitted — never written as zero.
type ComparableActuals struct {
	FreshInput *float64 `json:"fresh_input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

// Comparable is one finished story a gate may compare an estimate against.
type Comparable struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	Category   string            `json:"category,omitempty"`
	SharedTags int               `json:"shared_tags"`
	Actuals    ComparableActuals `json:"actuals"`
}

// ComparablesResult is the story-comparables response.
type ComparablesResult struct {
	StoryID     string       `json:"story_id"`
	Comparables []Comparable `json:"comparables"`
}

func storyComparables(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req comparablesReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	res, err := ComputeComparables(ctx, req.ID, req.Limit, req.IgnorePrefixes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

// ComputeComparables enumerates the stories comparable to storyID: finished
// ones that carry at least one measured token actual (the actual-* tags
// recordActual writes — the sole source, no dollar tag is read). It decides
// nothing; a gate judges an estimate against what this lists.
//
//   - Finished means terminal by the story's own workflow clock
//     (wfgovern.ClockFor via clockFor) — no status literal; an item whose
//     workflow does not resolve is skipped rather than guessed at.
//   - A container is excluded by having children (any item that is some
//     story's parent): its actual is a family roll-up, not a comparable.
//   - A story of category empty is excluded: it carries no product content,
//     so its cost says nothing about a story that does.
//   - Ranking: same category first, then count of tags shared with storyID
//     (minus ignorePrefixes), then most recently updated, then id.
//   - limit <= 0 returns every comparable.
func ComputeComparables(ctx context.Context, storyID string, limit int, ignorePrefixes []string) (ComparablesResult, error) {
	wi, err := requireWorkItem()
	if err != nil {
		return ComparablesResult{}, err
	}
	self, err := wi.Get(ctx, storyID)
	if err != nil {
		return ComparablesResult{}, err
	}
	all, err := wi.List(ctx, workitem.ListFilter{Kind: workitem.KindStory, Limit: 2000})
	if err != nil {
		return ComparablesResult{}, err
	}
	parents := map[string]bool{}
	for _, it := range all {
		if it.ParentID != "" {
			parents[it.ParentID] = true
		}
	}
	selfTags := map[string]bool{}
	for _, t := range self.Tags {
		if !hasAnyPrefix(t, ignorePrefixes) {
			selfTags[t] = true
		}
	}

	type clockKey struct{ workflow, category string }
	type resolved struct {
		clk costview.Clock
		ok  bool
	}
	clocks := map[clockKey]resolved{}

	type ranked struct {
		c       Comparable
		same    bool
		updated int64
	}
	var picked []ranked
	for _, it := range all {
		if it.ID == self.ID || parents[it.ID] || it.Category == workitem.EmptyCategory {
			continue
		}
		actuals := comparableActuals(it.Tags)
		if actuals == (ComparableActuals{}) {
			continue
		}
		key := clockKey{workflowStamp(it.Tags), it.Category}
		r, seen := clocks[key]
		if !seen {
			r.clk, r.ok = clockFor(ctx, it)
			clocks[key] = r
		}
		if !r.ok || r.clk.Terminal == nil || !r.clk.Terminal(it.Status) {
			continue
		}
		shared := 0
		counted := map[string]bool{}
		for _, t := range it.Tags {
			if selfTags[t] && !counted[t] {
				counted[t] = true
				shared++
			}
		}
		picked = append(picked, ranked{
			c: Comparable{
				ID: it.ID, Status: it.Status, Category: it.Category,
				SharedTags: shared, Actuals: actuals,
			},
			same:    it.Category == self.Category,
			updated: it.UpdatedAt.UnixNano(),
		})
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := picked[i], picked[j]
		if a.same != b.same {
			return a.same
		}
		if a.c.SharedTags != b.c.SharedTags {
			return a.c.SharedTags > b.c.SharedTags
		}
		if a.updated != b.updated {
			return a.updated > b.updated
		}
		return a.c.ID < b.c.ID
	})
	if limit > 0 && len(picked) > limit {
		picked = picked[:limit]
	}
	out := ComparablesResult{StoryID: self.ID, Comparables: make([]Comparable, 0, len(picked))}
	for _, p := range picked {
		out.Comparables = append(out.Comparables, p.c)
	}
	return out, nil
}

// comparableActuals maps the parsed actual-* tags onto ComparableActuals.
func comparableActuals(tags []string) ComparableActuals {
	var out ComparableActuals
	for _, a := range costview.ParseActuals(tags) {
		v := a.Value
		switch a.Unit {
		case "fresh-input":
			out.FreshInput = &v
		case "output":
			out.Output = &v
		case "cache-read":
			out.CacheRead = &v
		case "cache-write":
			out.CacheWrite = &v
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
