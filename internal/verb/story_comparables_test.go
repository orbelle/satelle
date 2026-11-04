package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// mkComparable creates one story in db with status, category, parent and tags,
// updated at a fixed offset so ranking ties are deterministic.
func mkComparable(t *testing.T, db *store.DB, title, status, category, parent string, ageMin int, tags ...string) workitem.Item {
	t.Helper()
	at := time.Unix(1_700_000_000, 0).Add(-time.Duration(ageMin) * time.Minute)
	it, err := db.Stories.Create(context.Background(), workitem.CreateInput{
		Kind: workitem.KindStory, Title: title, Status: status, Category: category, ParentID: parent, Tags: tags,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func comparableIDs(res verb.ComparablesResult) []string {
	var ids []string
	for _, c := range res.Comparables {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestComputeComparablesRankingAndFiltering pins the enumeration: finished
// means terminal by the workflow clock, containers are excluded by having
// children, the story itself and items with no measured actual are excluded,
// and the rest rank same category first, then by shared-tag count, then recency.
func TestComputeComparablesRankingAndFiltering(t *testing.T) {
	db := wireActualWF(t)
	ctx := context.Background()

	self := mkComparable(t, db, "self", "plan", "fix", "", 0, "epic:e", "sprint:s", "estimate-output:250000")

	otherCatManyShared := mkComparable(t, db, "feature two shared", "done", "feature", "", 5,
		"epic:e", "sprint:s", "actual-output:300000", "actual-fresh-input:9000")
	sameCatOneShared := mkComparable(t, db, "fix one shared", "done", "fix", "", 6,
		"epic:e", "actual-output:585123", "actual-fresh-input:unavailable", "actual-cache-read:7")
	sameCatNoShared := mkComparable(t, db, "fix no shared", "done", "fix", "", 1, "actual-output:100000")
	sameCatOneSharedOlder := mkComparable(t, db, "fix one shared older", "done", "fix", "", 60,
		"epic:e", "actual-output:700000")

	// Excluded: not terminal, no measured actual, dollar-only actual, container.
	mkComparable(t, db, "still open", "in_progress", "fix", "", 2, "epic:e", "actual-output:1")
	mkComparable(t, db, "no actual", "done", "fix", "", 2, "epic:e")
	mkComparable(t, db, "usd only", "done", "fix", "", 2, "epic:e", "actual-usd:86.73", "estimate-usd:25")
	container := mkComparable(t, db, "container", "done", "fix", "", 3, "epic:e", "actual-output:9999999")
	mkComparable(t, db, "child of container", "plan", "fix", container.ID, 3, "epic:e")

	res, err := verb.ComputeComparables(ctx, self.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Same category first (one shared before none, newer before older), then
	// the other category.
	want := []string{sameCatOneShared.ID, sameCatOneSharedOlder.ID, sameCatNoShared.ID, otherCatManyShared.ID}
	got := comparableIDs(res)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("comparables = %v, want %v", got, want)
	}
	if res.Comparables[0].SharedTags != 1 || res.Comparables[3].SharedTags != 2 {
		t.Errorf("shared counts wrong: %+v", res.Comparables)
	}

	// An unavailable figure is omitted, never zero; the measured ones are kept.
	a := res.Comparables[0].Actuals
	if a.FreshInput != nil || a.Output == nil || *a.Output != 585123 || a.CacheRead == nil || *a.CacheRead != 7 {
		t.Errorf("actuals for %s = %+v", sameCatOneShared.ID, a)
	}

	// No dollar figure reaches the output.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "usd") {
		t.Errorf("output carries a dollar figure: %s", raw)
	}

	// --limit caps after ranking.
	capped, err := verb.ComputeComparables(ctx, self.ID, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(comparableIDs(capped), ",") != strings.Join(want[:2], ",") {
		t.Errorf("limited = %v, want %v", comparableIDs(capped), want[:2])
	}
}

// TestComputeComparablesSkipsEmptyCategory pins that a finished story of
// category empty, measured actuals and all, is never a comparable — for an
// empty story or any other.
func TestComputeComparablesSkipsEmptyCategory(t *testing.T) {
	db := wireActualWF(t)
	ctx := context.Background()
	self := mkComparable(t, db, "self", "plan", "feature", "", 0, "epic:e")
	emptySelf := mkComparable(t, db, "empty self", "plan", workitem.EmptyCategory, "", 0, "epic:e")
	feature := mkComparable(t, db, "feature done", "done", "feature", "", 5, "epic:e", "actual-output:300000")
	mkComparable(t, db, "empty done", "done", workitem.EmptyCategory, "", 1, "epic:e", "actual-output:4000")

	for _, s := range []workitem.Item{self, emptySelf} {
		res, err := verb.ComputeComparables(ctx, s.ID, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := comparableIDs(res); strings.Join(got, ",") != feature.ID {
			t.Errorf("comparables for %s (%s) = %v, want only %s", s.ID, s.Category, got, feature.ID)
		}
	}
}

// TestComputeComparablesIgnorePrefixes pins that the shared-tag count ignores
// nothing by default and only the prefixes the caller passes.
func TestComputeComparablesIgnorePrefixes(t *testing.T) {
	db := wireActualWF(t)
	ctx := context.Background()
	self := mkComparable(t, db, "self", "plan", "fix", "", 0, "estimate-output:250000", "order:3")
	other := mkComparable(t, db, "other", "done", "fix", "", 5, "estimate-output:250000", "order:3", "actual-output:10")

	res, err := verb.ComputeComparables(ctx, self.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Comparables) != 1 || res.Comparables[0].ID != other.ID || res.Comparables[0].SharedTags != 2 {
		t.Fatalf("default (no ignore) = %+v, want shared_tags 2", res.Comparables)
	}
	res, err = verb.ComputeComparables(ctx, self.ID, 0, []string{"estimate-", "order:"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Comparables[0].SharedTags != 0 {
		t.Fatalf("with ignore prefixes shared_tags = %d, want 0", res.Comparables[0].SharedTags)
	}
}

// TestStoryComparablesVerb pins the registered verb's request/response seam.
func TestStoryComparablesVerb(t *testing.T) {
	db := wireActualWF(t)
	self := mkComparable(t, db, "self", "plan", "fix", "", 0, "epic:e")
	other := mkComparable(t, db, "other", "done", "fix", "", 5, "epic:e", "actual-output:42")

	raw, err := verb.Dispatch(context.Background(), "story-comparables", json.RawMessage(`{"id":"`+self.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var res verb.ComparablesResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.StoryID != self.ID || len(res.Comparables) != 1 || res.Comparables[0].ID != other.ID {
		t.Fatalf("response = %s", raw)
	}
	if _, err := verb.Dispatch(context.Background(), "story-comparables", json.RawMessage(`{}`)); err == nil {
		t.Error("a request with no id must be refused")
	}
}
