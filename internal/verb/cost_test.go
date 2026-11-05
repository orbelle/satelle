package verb_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func TestStoryEstimateWritesTagsInAnyUnit(t *testing.T) {
	wire(t)

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "tags": []string{"area:web"}}), &it)

	// The legacy units still write, preserving area:web.
	var est workitem.Item
	json.Unmarshal(call(t, "story-estimate", map[string]any{"id": it.ID, "time": "30m", "tokens": 50000, "basis": "rough"}), &est)
	if !hasTag(est.Tags, "estimate-minutes:30") || !hasTag(est.Tags, "estimate-tokens:50000") {
		t.Fatalf("estimate tags missing: %v", est.Tags)
	}
	if !hasTag(est.Tags, "area:web") {
		t.Errorf("estimate dropped an unrelated tag: %v", est.Tags)
	}

	// The current units — fresh-input, output — write alongside the legacy
	// ones. A dollar figure in the request is not an estimate and writes nothing.
	var cur workitem.Item
	json.Unmarshal(call(t, "story-estimate", map[string]any{"id": it.ID, "usd": 2.5, "fresh_input": 100000, "output": 20000}), &cur)
	for _, want := range []string{"estimate-fresh-input:100000", "estimate-output:20000", "estimate-minutes:30", "estimate-tokens:50000"} {
		if !hasTag(cur.Tags, want) {
			t.Errorf("after fresh-input/output estimate, missing tag %q in %v", want, cur.Tags)
		}
	}
	if hasTag(cur.Tags, "estimate-usd:2.5") {
		t.Errorf("a dollar figure must not be written as estimate-usd: %v", cur.Tags)
	}

	// Re-recording an estimate replaces the prior value rather than duplicating it.
	var re workitem.Item
	json.Unmarshal(call(t, "story-estimate", map[string]any{"id": it.ID, "tokens": 60000}), &re)
	count := 0
	for _, tg := range re.Tags {
		if len(tg) >= 15 && tg[:15] == "estimate-tokens" {
			count++
		}
	}
	if count != 1 || !hasTag(re.Tags, "estimate-tokens:60000") {
		t.Errorf("re-record should replace estimate-tokens (one, =60000), got %v", re.Tags)
	}

	// Every recording left an estimate_recorded ledger row.
	var entries []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it.ID, "kind": ledger.KindEstimateRecorded}), &entries)
	if len(entries) != 3 {
		t.Errorf("want 3 estimate_recorded rows, got %d", len(entries))
	}
}

// TestStoryActualUnpricedRowsTagUnavailable pins that a measured row with no
// cost_usd is still banded from its tokens. A dollar figure is not an input:
// the absence of a price does not withhold the band, and the recorder does
// not write actual-usd.
func TestStoryActualUnpricedRowsTagUnavailable(t *testing.T) {
	db := wire(t)
	ctx := context.Background()

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "unpriced-driven"}), &it)

	payload, _ := json.Marshal(map[string]any{
		"from": "plan", "to": "in_progress", "agent": "coder",
		"usage_available": true, "tokens_in_fresh": 1000, "tokens_out": 200, "duration_ms": 5000,
		// no cost_usd — the adapter never reported a price.
	})
	if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: it.ID, Kind: ledger.KindAgentInvocation, Actor: "coder", Payload: payload,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	var act workitem.Item
	json.Unmarshal(call(t, "story-actual", map[string]any{"id": it.ID}), &act)
	if !hasTag(act.Tags, "actual-cost:low") {
		t.Errorf("unpriced measured tokens must be actual-cost:low: %v", act.Tags)
	}
	if hasActualUSD(act.Tags) {
		t.Errorf("actual recorder must not write actual-usd: %v", act.Tags)
	}
	if !hasTag(act.Tags, "actual-cost-unavailable-rows:1") {
		t.Errorf("want actual-cost-unavailable-rows:1, got %v", act.Tags)
	}
}

func hasActualUSD(tags []string) bool {
	for _, t := range tags {
		if t == "actual-usd" || strings.HasPrefix(t, "actual-usd:") {
			return true
		}
	}
	return false
}

func actualCostBands(tags []string) []string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, "actual-cost:") {
			out = append(out, t)
		}
	}
	return out
}

func appendInvocationRow(t *testing.T, db *store.DB, storyID string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: storyID, Kind: ledger.KindAgentInvocation, Actor: "coder", Payload: raw,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestStoryActualBandIgnoresCostUSD pins dollar independence at the recorder:
// two stories with the same token counts and different cost_usd get the same
// band, and neither is tagged actual-usd.
func TestStoryActualBandIgnoresCostUSD(t *testing.T) {
	db := wire(t)
	for _, cost := range []float64{0.01, 999.0} {
		var it workitem.Item
		json.Unmarshal(call(t, "story-create", map[string]any{"title": "priced"}), &it)
		appendInvocationRow(t, db, it.ID, map[string]any{
			"from": "plan", "to": "in_progress", "agent": "coder",
			"usage_available": true, "tokens_in_fresh": 1000, "tokens_out": 200,
			"duration_ms": 5000, "cost_usd": cost,
		})
		var act workitem.Item
		json.Unmarshal(call(t, "story-actual", map[string]any{"id": it.ID}), &act)
		if bands := actualCostBands(act.Tags); len(bands) != 1 || bands[0] != "actual-cost:low" {
			t.Errorf("cost_usd %v: bands = %v, want exactly actual-cost:low", cost, act.Tags)
		}
		if hasActualUSD(act.Tags) {
			t.Errorf("cost_usd %v: actual recorder wrote actual-usd: %v", cost, act.Tags)
		}
	}
}

// TestStoryActualNoUsageRowsWritesNoBand pins that a priced row which reported
// no token usage writes no band, and does not rewrite a stored actual-usd tag.
func TestStoryActualNoUsageRowsWritesNoBand(t *testing.T) {
	db := wire(t)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{
		"title": "usage-less",
		"tags":  []string{"actual-cost:high", "actual-usd:9.99", "area:web"},
	}), &it)
	appendInvocationRow(t, db, it.ID, map[string]any{
		"from": "plan", "to": "in_progress", "agent": "coder",
		"usage_available": false, "cost_usd": 1.25, "duration_ms": 5000,
	})
	var act workitem.Item
	json.Unmarshal(call(t, "story-actual", map[string]any{"id": it.ID}), &act)
	if bands := actualCostBands(act.Tags); len(bands) != 0 {
		t.Errorf("no measured tokens must write no actual-cost tag, got %v", act.Tags)
	}
	if !hasTag(act.Tags, "actual-usd:9.99") {
		t.Errorf("stored actual-usd must be left as stored, got %v", act.Tags)
	}
	if !hasTag(act.Tags, "area:web") {
		t.Errorf("unrelated tag dropped: %v", act.Tags)
	}
	if !hasTag(act.Tags, "actual-tokens:0") {
		t.Errorf("token accounting must still be written, got %v", act.Tags)
	}
}

// TestRecordActualBandBoundaries pins both sides of the low/medium boundary
// and the high boundary at the recorder, composing fresh + unsplit + output +
// cache read + cache write. Unsplit input is its own legacy row: addDispatchRow
// drops TokensIn when fresh or cache is set on the same row.
func TestRecordActualBandBoundaries(t *testing.T) {
	db := wire(t)
	// Constant parts of the sum: unsplit 30000 + output 15000 + cache read 4000
	// + cache write 1000 = 50000. fresh_input is the field that crosses.
	const (
		unsplit    = 30000
		output     = 15000
		cacheRead  = 4000
		cacheWrite = 1000
		rest       = unsplit + output + cacheRead + cacheWrite
	)
	cases := []struct {
		fresh int
		want  string
	}{
		{199999 - rest, "low"},    // 149999 + 50000 = 199999
		{200000 - rest, "medium"}, // 150000 + 50000 = 200000
		{2000000 - rest, "high"},  // 1950000 + 50000 = 2000000
	}
	for _, c := range cases {
		var it workitem.Item
		json.Unmarshal(call(t, "story-create", map[string]any{"title": "boundary"}), &it)
		appendInvocationRow(t, db, it.ID, map[string]any{
			"from": "plan", "to": "in_progress", "agent": "coder",
			"usage_available": true,
			"tokens_in_fresh": c.fresh, "tokens_out": output,
			"tokens_cache_read": cacheRead, "tokens_cache_write": cacheWrite,
			"duration_ms": 1000, "cost_usd": 4.5,
		})
		// Legacy row: TokensIn only. No fresh/cache fields, or this input is dropped.
		appendInvocationRow(t, db, it.ID, map[string]any{
			"from": "plan", "to": "in_progress", "agent": "coder",
			"usage_available": true, "tokens_in": unsplit, "tokens_out": 0, "duration_ms": 1000,
		})
		var act workitem.Item
		json.Unmarshal(call(t, "story-actual", map[string]any{"id": it.ID}), &act)
		want := "actual-cost:" + c.want
		if bands := actualCostBands(act.Tags); len(bands) != 1 || bands[0] != want {
			t.Errorf("fresh %d (total %d): bands = %v, want exactly %s; tags %v", c.fresh, c.fresh+rest, bands, want, act.Tags)
		}
		if !hasTag(act.Tags, "actual-unsplit-input:30000") {
			t.Errorf("fresh %d: legacy unsplit row did not land: %v", c.fresh, act.Tags)
		}
		if hasActualUSD(act.Tags) {
			t.Errorf("fresh %d: actual recorder wrote actual-usd: %v", c.fresh, act.Tags)
		}
	}
}

// TestStoryActualUnsplitInputTagged pins revision 2 point 2: a legacy row
// recorded before the fresh/cache split existed must not vanish from the
// actual — it is tagged actual-unsplit-input and folded into actual-tokens,
// and actual-fresh-input reads "unavailable" rather than a false 0 when every
// input row is unsplit.
func TestStoryActualUnsplitInputTagged(t *testing.T) {
	db := wire(t)
	ctx := context.Background()

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "unsplit"}), &it)

	payload, _ := json.Marshal(map[string]any{
		"from": "plan", "to": "in_progress", "agent": "coder",
		"usage_available": true, "tokens_in": 300, "tokens_out": 50, "duration_ms": 1000, "cost_usd": 0.05,
		// no tokens_in_fresh/tokens_cache_write/tokens_cache_read — unsplit.
	})
	if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: it.ID, Kind: ledger.KindAgentInvocation, Actor: "coder", Payload: payload,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	var act workitem.Item
	json.Unmarshal(call(t, "story-actual", map[string]any{"id": it.ID}), &act)
	if !hasTag(act.Tags, "actual-unsplit-input:300") {
		t.Errorf("want actual-unsplit-input:300, got %v", act.Tags)
	}
	if !hasTag(act.Tags, "actual-fresh-input:unavailable") {
		t.Errorf("an all-unsplit input must read actual-fresh-input:unavailable, not 0: %v", act.Tags)
	}
	if !hasTag(act.Tags, "actual-tokens:350") {
		t.Errorf("want actual-tokens:350 (unsplit input folded in), got %v", act.Tags)
	}
}

// TestStoryActualRefusesCallerFigures pins AC1: story-actual takes no
// figures — it is computed, never hand-entered.
func TestStoryActualRefusesCallerFigures(t *testing.T) {
	wire(t)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x"}), &it)
	for _, req := range []map[string]any{
		{"id": it.ID, "tokens": 200000},
		{"id": it.ID, "time": "30m"},
		{"id": it.ID, "usd": 1.0},
		{"id": it.ID, "fresh_input": 100},
		{"id": it.ID, "output": 100},
	} {
		if _, err := dispatchRaw(t, "story-actual", req); err == nil {
			t.Errorf("story-actual with %v should be refused", req)
		} else if !strings.Contains(err.Error(), "computed from the ledger") {
			t.Errorf("story-actual with %v: err = %v, want it to name the computed replacement", req, err)
		}
	}
}

func TestStoryEstimateRequiresAValue(t *testing.T) {
	wire(t)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x"}), &it)
	if _, err := dispatchRaw(t, "story-estimate", map[string]any{"id": it.ID}); err == nil {
		t.Error("estimate with neither tokens nor time should error")
	}
}

// sty_38915987 AC3: drive refusal THROUGH story-estimate / story-actual.
// Move status between the verb's Get and Update via SetAfterTagCASGetHook so
// the test fails if ExpectStatus is removed from storyEstimate.
func TestStoryEstimateRefusesWhenStatusMoved(t *testing.T) {
	withWiring(t)
	db := wire(t)
	ctx := context.Background()
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{
		"title": "race-est", "tags": []string{"keep-me"},
	}), &it)

	verb.SetAfterTagCASGetHook(func(c context.Context, id, statusAtGet string) {
		if statusAtGet != workitem.StatusBacklog {
			t.Errorf("estimate Get saw status %q, want backlog", statusAtGet)
		}
		if _, err := db.Stories.SetStatus(c, id, "in_progress", time.Now()); err != nil {
			t.Errorf("SetStatus under estimate: %v", err)
		}
	})

	_, err := dispatchRaw(t, "story-estimate", map[string]any{"id": it.ID, "time": "30m"})
	if !errors.Is(err, workitem.ErrStatusConflict) {
		t.Fatalf("story-estimate err = %v, want ErrStatusConflict", err)
	}
	got, gerr := db.Stories.Get(ctx, it.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != "in_progress" {
		t.Errorf("status = %q after refused estimate, want in_progress", got.Status)
	}
	if hasTag(got.Tags, "estimate-minutes:30") {
		t.Errorf("refused estimate leaked tag: %v", got.Tags)
	}
	if !hasTag(got.Tags, "keep-me") {
		t.Errorf("unrelated tags changed: %v", got.Tags)
	}
}

// sty_38915987 AC3: story-actual must refuse the same Get→Update race — even
// though the actual is now computed rather than passed in (sty_8eae81ac AC1),
// recordActual's own Get→Update still goes through the same CAS.
func TestStoryActualRefusesWhenStatusMoved(t *testing.T) {
	withWiring(t)
	db := wire(t)
	ctx := context.Background()
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{
		"title": "race-act", "tags": []string{"keep-me"},
	}), &it)

	verb.SetAfterTagCASGetHook(func(c context.Context, id, statusAtGet string) {
		if statusAtGet != workitem.StatusBacklog {
			t.Errorf("actual Get saw status %q, want backlog", statusAtGet)
		}
		if _, err := db.Stories.SetStatus(c, id, "in_progress", time.Now()); err != nil {
			t.Errorf("SetStatus under actual: %v", err)
		}
	})

	_, err := dispatchRaw(t, "story-actual", map[string]any{"id": it.ID})
	if !errors.Is(err, workitem.ErrStatusConflict) {
		t.Fatalf("story-actual err = %v, want ErrStatusConflict", err)
	}
	got, gerr := db.Stories.Get(ctx, it.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != "in_progress" {
		t.Errorf("status = %q after refused actual, want in_progress", got.Status)
	}
	if hasTag(got.Tags, "actual-minutes:45") {
		t.Errorf("refused actual leaked tag: %v", got.Tags)
	}
	if !hasTag(got.Tags, "keep-me") {
		t.Errorf("unrelated tags changed: %v", got.Tags)
	}
}

// sty_ef8a896b AC2: the unitless and suffixed paths both land through
// parseCostDuration, so `--time 38` records 38 minutes on an estimate, while a
// malformed value is refused with a usable example.
func TestStoryCostTimeAcceptsUnitlessMinutes(t *testing.T) {
	wire(t)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "units"}), &it)

	var est workitem.Item
	json.Unmarshal(call(t, "story-estimate", map[string]any{"id": it.ID, "time": "38"}), &est)
	if !hasTag(est.Tags, "estimate-minutes:38") {
		t.Errorf(`estimate --time "38" tags = %v, want estimate-minutes:38`, est.Tags)
	}
	// The suffixed path is unchanged.
	var suffixed workitem.Item
	json.Unmarshal(call(t, "story-estimate", map[string]any{"id": it.ID, "time": "2h"}), &suffixed)
	if !hasTag(suffixed.Tags, "estimate-minutes:120") {
		t.Errorf(`estimate --time "2h" tags = %v, want estimate-minutes:120`, suffixed.Tags)
	}

	_, err := dispatchRaw(t, "story-estimate", map[string]any{"id": it.ID, "time": "38x"})
	if err == nil {
		t.Fatal(`estimate --time "38x" should error`)
	}
	for _, want := range []string{"30m", "38"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should carry the example %q", err, want)
		}
	}
}
