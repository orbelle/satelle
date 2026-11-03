package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A story's status, definition and park origin are what folding its ledger chain
// yields (sty_d84be8bc); the work_items row is a cache of that fold. This file is
// the verb layer's one seam onto it: reads that need the answer go through
// storyState / overlayFold, a mutation ends in rebuildCache, and reconcile and the
// push check compare the row with the fold through foldStory.

// TransitionTo reads the destination status from a status_transition event.
func TransitionTo(e ledger.Entry) string { return storyfold.TransitionTo(e) }

// TransitionFrom reads the source status from a status_transition event.
func TransitionFrom(e ledger.Entry) string { return storyfold.TransitionFrom(e) }

// foldable reports whether item's state is carried by its chain. Only a story is;
// a task's file and an execution's run are read from their rows as before.
func foldable(item workitem.Item) bool { return item.Kind == workitem.KindStory }

// routeFor classifies park and final states for item from its governing route;
// the zero Route when none resolves, in which case the fold leaves park origin to
// the events that carry one and skips no stale transition.
func routeFor(ctx context.Context, item workitem.Item) storyfold.Route {
	idx, err := requireDocIndex()
	if err != nil {
		return storyfold.Route{}
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return storyfold.Route{}
	}
	spec, _, _, serr := wfgovern.SpecFor(wfs, item)
	if serr != nil {
		return storyfold.Route{}
	}
	return storyfold.Route{Park: spec.IsParkState, Final: spec.IsFinal}
}

// foldRoute is routeFor for a story known by its chain: the route is chosen from
// the category and tags the chain says it has, not the row's.
func foldRoute(ctx context.Context, row workitem.Item, chain []ledger.Entry) storyfold.Route {
	st, _ := storyfold.Fold(chain, storyfold.Route{})
	return routeFor(ctx, storyfold.Overlay(row, st))
}

// FoldChain folds a chain that is not the local one (a hosted chain decoded to
// entries) with the route FoldStory would choose, so the status it reports is the
// status the story would have if that chain were its own.
func FoldChain(ctx context.Context, row workitem.Item, chain []ledger.Entry) storyfold.State {
	st, _ := storyfold.Fold(chain, foldRoute(ctx, row, chain))
	return st
}

// FoldStory folds row's chain read from led. The route that classifies park
// states is chosen from the story as its chain says it is, not as the row says,
// so a corrupted row cannot steer its own fold.
func FoldStory(ctx context.Context, led *ledger.Store, row workitem.Item) (storyfold.State, []ledger.ChainBreak, error) {
	chain, err := led.StoryChain(ctx, row.ID)
	if err != nil {
		return storyfold.State{}, nil, err
	}
	st, breaks := storyfold.Fold(chain, foldRoute(ctx, row, chain))
	return st, breaks, nil
}

// foldedItem is row with every field its chain defines replaced by the chain's
// value. A non-story, or a repo with no ledger wired, is returned as it is.
func foldedItem(ctx context.Context, row workitem.Item) (workitem.Item, error) {
	if !foldable(row) || ledgerStore == nil {
		return row, nil
	}
	st, _, err := FoldStory(ctx, ledgerStore, row)
	if err != nil {
		return row, err
	}
	return storyfold.Overlay(row, st), nil
}

// storyState reads one item with its status, definition and park origin taken
// from the fold of its ledger chain, not from the cached row. Row-only fields
// (priority, parent, assignee, timestamps) stay the row's.
func storyState(ctx context.Context, id string) (workitem.Item, error) {
	store, err := requireWorkItem()
	if err != nil {
		return workitem.Item{}, err
	}
	row, err := store.Get(ctx, id)
	if err != nil {
		return workitem.Item{}, err
	}
	return foldedItem(ctx, row)
}

// overlayFold is storyState over items already read.
func overlayFold(ctx context.Context, items []workitem.Item) ([]workitem.Item, error) {
	out := make([]workitem.Item, len(items))
	for i, it := range items {
		f, err := foldedItem(ctx, it)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// storyStateHealed is storyState for a mutation: when the cached row has drifted
// from the fold it is rebuilt first, so the compare-and-set a write carries holds
// against the status the chain says rather than the one a corrupted row says.
func storyStateHealed(ctx context.Context, id string) (workitem.Item, error) {
	store, err := requireWorkItem()
	if err != nil {
		return workitem.Item{}, err
	}
	row, err := store.Get(ctx, id)
	if err != nil {
		return workitem.Item{}, err
	}
	folded, err := foldedItem(ctx, row)
	if err != nil || !foldable(row) {
		return folded, err
	}
	if _, derr := rebuildCache(ctx, id); derr != nil {
		return folded, derr
	}
	return folded, nil
}

// updateFromFold is the row update that makes row agree with st on the given
// drifted fields.
func updateFromFold(drifts []storyfold.Drift, st storyfold.State) workitem.UpdateInput {
	var in workitem.UpdateInput
	for _, d := range drifts {
		switch d.Field {
		case storyfold.FieldStatus:
			in.Status = &st.Status
		case storyfold.FieldTitle:
			in.Title = &st.Title
		case storyfold.FieldBody:
			in.Body = &st.Body
		case storyfold.FieldAcceptance:
			in.AcceptanceCriteria = &st.AcceptanceCriteria
		case storyfold.FieldCategory:
			in.Category = &st.Category
		case storyfold.FieldTags:
			tags := append([]string{}, st.Tags...)
			in.Tags = &tags
		case storyfold.FieldParkOrigin:
			in.ParkOrigin = &st.ParkOrigin
		case storyfold.FieldArchived:
			in.Archived = &st.Archived
		}
	}
	return in
}

// rebuildCache rewrites the cached row of a story from the fold of its chain, on
// the fields the chain defines and the row disagrees on, and returns the row as
// it now stands. Nothing is written when the row already agrees, so a settled
// story's updated_at does not move. It is the last step of every story mutation
// and of a sync pull that applied a story's events.
func rebuildCache(ctx context.Context, id string) (workitem.Item, error) {
	store, err := requireWorkItem()
	if err != nil {
		return workitem.Item{}, err
	}
	row, err := store.Get(ctx, id)
	if err != nil {
		return workitem.Item{}, err
	}
	if !foldable(row) || ledgerStore == nil {
		return row, nil
	}
	return RebuildRow(ctx, store, ledgerStore, row, time.Now())
}

// RebuildRow is rebuildCache for a caller that holds the stores and the row: the
// sync pull, which rebuilds each story it applied.
func RebuildRow(ctx context.Context, store *workitem.Store, led *ledger.Store, row workitem.Item, now time.Time) (workitem.Item, error) {
	st, _, err := FoldStory(ctx, led, row)
	if err != nil {
		return row, err
	}
	drifts := storyfold.Compare(row, st)
	if len(drifts) == 0 {
		return row, nil
	}
	return store.Update(ctx, row.ID, updateFromFold(drifts, st), now)
}

// StoryDrift is one folded field on which a story's cached row disagrees with its
// chain. Chain is the value that is right.
type StoryDrift struct {
	storyfold.Drift
	Title string `json:"title"`
}

// DetectDrift compares every story's cached row with the fold of its chain and
// returns each field that disagrees. Tasks and executions are not folded.
func DetectDrift(ctx context.Context, stories *workitem.Store, led *ledger.Store) ([]StoryDrift, error) {
	if stories == nil || led == nil {
		return nil, fmt.Errorf("verb: detect drift needs stories and ledger stores")
	}
	items, err := stories.List(ctx, workitem.ListFilter{Kind: workitem.KindStory, Limit: 2000, IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	var out []StoryDrift
	for _, it := range items {
		ds, err := StoryDrifts(ctx, led, it)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}
	return out, nil
}

// StoryDrifts lists the fields on which one story's row disagrees with its fold.
func StoryDrifts(ctx context.Context, led *ledger.Store, row workitem.Item) ([]StoryDrift, error) {
	if !foldable(row) {
		return nil, nil
	}
	st, _, err := FoldStory(ctx, led, row)
	if err != nil {
		return nil, err
	}
	var out []StoryDrift
	for _, d := range storyfold.Compare(row, st) {
		out = append(out, StoryDrift{Drift: d, Title: row.Title})
	}
	return out, nil
}

// DetectStaleTransitions lists, across every story, the status transitions the
// fold skips because their from is not the status their chain had reached at
// their place. They are facts of an append-only chain, not drift: no row rebuild
// removes them, so DetectDrift does not carry them.
func DetectStaleTransitions(ctx context.Context, stories *workitem.Store, led *ledger.Store) ([]storyfold.ReplayBreak, error) {
	if stories == nil || led == nil {
		return nil, fmt.Errorf("verb: detect stale transitions needs stories and ledger stores")
	}
	items, err := stories.List(ctx, workitem.ListFilter{Kind: workitem.KindStory, Limit: 2000, IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	var out []storyfold.ReplayBreak
	for _, it := range items {
		st, _, err := FoldStory(ctx, led, it)
		if err != nil {
			return nil, err
		}
		out = append(out, st.StaleTransitions...)
	}
	return out, nil
}

// DriftOf keeps the drifts of the stories named in only. A nil only keeps all.
func DriftOf(drifts []StoryDrift, only map[string]bool) []StoryDrift {
	if only == nil {
		return drifts
	}
	var out []StoryDrift
	for _, d := range drifts {
		if only[d.ID] {
			out = append(out, d)
		}
	}
	return out
}

// RepairDrift rebuilds every drifted story's row from its fold and appends a
// status_reconcile event naming each repaired field. It returns what it repaired.
func RepairDrift(ctx context.Context, stories *workitem.Store, led *ledger.Store, now time.Time) (repaired []StoryDrift, err error) {
	return RepairDriftOf(ctx, stories, led, now, nil)
}

// RepairDriftOf is RepairDrift limited to the stories named in only; a drifted
// story outside it is left as it is. A nil only repairs every drifted story.
func RepairDriftOf(ctx context.Context, stories *workitem.Store, led *ledger.Store, now time.Time, only map[string]bool) (repaired []StoryDrift, err error) {
	drifts, err := DetectDrift(ctx, stories, led)
	if err != nil {
		return nil, err
	}
	drifts = DriftOf(drifts, only)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	byStory := map[string][]StoryDrift{}
	var order []string
	for _, d := range drifts {
		if _, seen := byStory[d.ID]; !seen {
			order = append(order, d.ID)
		}
		byStory[d.ID] = append(byStory[d.ID], d)
	}
	for _, id := range order {
		row, gerr := stories.Get(ctx, id)
		if gerr != nil {
			return repaired, fmt.Errorf("repair %s: %w", id, gerr)
		}
		if _, rerr := RebuildRow(ctx, stories, led, row, now); rerr != nil {
			return repaired, fmt.Errorf("repair %s: %w", id, rerr)
		}
		fields := make([]string, 0, len(byStory[id]))
		for _, d := range byStory[id] {
			fields = append(fields, d.Field)
		}
		payload := reconcilePayload(byStory[id])
		if _, aerr := led.Append(ctx, ledger.AppendInput{
			StoryID: id, Kind: ledger.KindStatusReconcile, Actor: "reconcile",
			Body:    fmt.Sprintf("reconciled %s from the ledger chain", strings.Join(fields, ", ")),
			Payload: payload,
		}, now); aerr != nil {
			return repaired, fmt.Errorf("repair %s: record: %w", id, aerr)
		}
		repaired = append(repaired, byStory[id]...)
	}
	return repaired, nil
}

// tagsEqual reports whether two tag lists are the same list, in order.
func tagsEqual(a, b []string) bool { return slices.Equal(a, b) }

// StoryCreatedPayload is the payload of a story's story_created event: its whole
// definition and the status the create committed, beside the source that made it.
func StoryCreatedPayload(it workitem.Item, source string) json.RawMessage {
	return storyfold.CreatedPayloadOf(it, source)
}

// AppendStoryCreated records the story_created event of a story, with the payload
// the fold reads. It is the one place that event is written: the create verb and
// the reindex fix-it story both call it.
func AppendStoryCreated(ctx context.Context, led *ledger.Store, it workitem.Item, actor, source string, now time.Time) error {
	if led == nil {
		return nil
	}
	_, err := led.Append(ctx, ledger.AppendInput{
		StoryID: it.ID, Kind: ledger.KindStoryCreated, Actor: actor,
		Body:    fmt.Sprintf("created %s %q", it.Kind, it.Title),
		Payload: StoryCreatedPayload(it, source),
	}, now)
	return err
}

// tagsChangedBody is the one-line summary of a tag change, for a ledger listing.
func tagsChangedBody(before, after []string) string {
	have := map[string]bool{}
	for _, t := range before {
		have[t] = true
	}
	var added, removed []string
	for _, t := range after {
		if !have[t] {
			added = append(added, "+"+t)
		}
		delete(have, t)
	}
	for _, t := range before {
		if have[t] {
			removed = append(removed, "-"+t)
		}
	}
	return "tags changed: " + strings.Join(append(added, removed...), " ")
}

// tagChange is the story_updated payload that names a tag change: the tags before
// and after, and the writer that made it. Every writer of a story's tags records
// it in this one shape, so the fold needs one reader.
func tagChange(before, after []string, source string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"tags":   storyfold.TagsChange{Before: nonNil(before), After: nonNil(after)},
		"source": source,
	})
	return b
}

func nonNil(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// TagChangeInput is the ledger input that records a change to a story's tags: a
// story_updated event naming the tags before and after. It is the one builder of
// that event, for the verbs here and for a caller that appends in its own
// transaction (the migrate restamp).
func TagChangeInput(id string, before, after []string, source, actor string) ledger.AppendInput {
	return ledger.AppendInput{
		StoryID: id, Kind: ledger.KindStoryUpdated, Actor: actor,
		Body: tagsChangedBody(before, after), Payload: tagChange(before, after, source),
	}
}

// HostedWinsInput is the story_updated event that lets a hosted copy win over the
// chain a forced pull merged it into (sty_d84be8bc). A ledger row is never
// rewritten, so a local-only edit stays in the chain; to make the hosted
// definition and tags the story's, the pull appends the edit that sets them back,
// naming source. It returns false when the chain already says what hosted does.
// Status is not set this way: it is moved only by transitions.
func HostedWinsInput(st storyfold.State, hosted workitem.Item, source string) (ledger.AppendInput, bool) {
	var fields []AmendField
	add := func(field, chain, host string) {
		if st.Defined[field] && chain != host {
			fields = append(fields, AmendField{Field: field, Old: chain, New: host})
		}
	}
	add(storyfold.FieldTitle, st.Title, hosted.Title)
	add(storyfold.FieldBody, st.Body, hosted.Body)
	add(storyfold.FieldAcceptance, st.AcceptanceCriteria, hosted.AcceptanceCriteria)
	add(storyfold.FieldCategory, st.Category, hosted.Category)
	var tags *storyfold.TagsChange
	if st.Defined[storyfold.FieldTags] && !tagsEqual(st.Tags, hosted.Tags) {
		tags = &storyfold.TagsChange{Before: nonNil(st.Tags), After: nonNil(hosted.Tags)}
	}
	if len(fields) == 0 && tags == nil {
		return ledger.AppendInput{}, false
	}
	payload, err := json.Marshal(storyUpdatedPayload{Fields: fields, Tags: tags, Source: source})
	if err != nil {
		return ledger.AppendInput{}, false
	}
	return ledger.AppendInput{
		StoryID: hosted.ID, Kind: ledger.KindStoryUpdated, Actor: "sync",
		Body: "hosted copy won: " + source, Payload: payload,
	}, true
}

// appendTagChange records a change to a story's tags as a story_updated event
// naming the tags before and after, and nothing when they did not change.
// Best-effort, like every other trail row.
func appendTagChange(ctx context.Context, id string, before, after []string, source, actor string, now time.Time) {
	if tagsEqual(before, after) || ledgerStore == nil {
		return
	}
	_, _ = ledgerStore.Append(ctx, TagChangeInput(id, before, after, source, actor), now)
}
