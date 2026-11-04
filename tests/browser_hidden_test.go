//go:build integration

package tests

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestBrowserHiddenPageDefersLiveRefetch drives sty_a003a459 AC3 in headless
// Chrome: while the board is hidden (document.hidden), SSE triggers fetch no
// fragment; when it is shown again, exactly one reconcile runs and brings in
// what changed meanwhile.
func TestBrowserHiddenPageDefersLiveRefetch(t *testing.T) {
	base, repo := serveRepo(t, "8861")
	ctx := newChrome(t)
	browserStep(t, ctx, "load board",
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
	)
	if !waitCond(t, ctx, `window.__satelleLive.leader && window.__satelleLive.open`, 5*time.Second) {
		t.Fatal("a lone page should lead the shared /events stream")
	}

	// Count fragment fetches, and hide the page the way the browser reports it.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		window.__fragFetches = 0;
		var f = window.fetch;
		window.fetch = function(u){ if (String(u).indexOf("fragment/") >= 0) window.__fragFetches++; return f.apply(this, arguments); };
		window.__hidden = true;
		Object.defineProperty(document, "hidden", {configurable: true, get: function(){ return window.__hidden; }});
		Object.defineProperty(document, "visibilityState", {configurable: true, get: function(){ return window.__hidden ? "hidden" : "visible"; }});
		document.dispatchEvent(new Event("visibilitychange"));
		return true;
	})()`, nil)); err != nil {
		t.Fatal(err)
	}
	var before struct{ Triggers, Reconciles int }
	if err := chromedp.Run(ctx, chromedp.Evaluate(`({triggers: window.__satelleLive.triggers, reconciles: window.__satelleLive.reconciles})`, &before)); err != nil {
		t.Fatal(err)
	}

	id := createStory(t, repo, "Changed While Hidden", "")
	if !waitCond(t, ctx, `window.__satelleLive.triggers > `+strconv.Itoa(before.Triggers), 5*time.Second) {
		t.Fatal("no live trigger reached the hidden page")
	}
	time.Sleep(time.Second) // time-subject: past the 250ms refetch debounce, so a deferred refetch would have fired
	var hidden struct{ Fetches, Reconciles int }
	if err := chromedp.Run(ctx, chromedp.Evaluate(`({fetches: window.__fragFetches, reconciles: window.__satelleLive.reconciles})`, &hidden)); err != nil {
		t.Fatal(err)
	}
	if hidden.Fetches != 0 {
		t.Fatalf("the hidden page fetched %d fragment(s), want 0", hidden.Fetches)
	}
	if hidden.Reconciles != before.Reconciles {
		t.Fatalf("the hidden page reconciled %d time(s), want 0", hidden.Reconciles-before.Reconciles)
	}
	if visibleRowNow(t, ctx, id) {
		t.Fatal("fixture: the hidden page rendered the new story")
	}

	show := `(function(){ window.__hidden = false; document.dispatchEvent(new Event("visibilitychange")); return true; })()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(show, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitCond(t, ctx, jsRowVisible(id), 5*time.Second) {
		t.Fatalf("showing the page did not bring in story %s", id)
	}
	// A second visibilitychange with nothing new must not reconcile again.
	if err := chromedp.Run(ctx, chromedp.Evaluate(show, nil)); err != nil {
		t.Fatal(err)
	}
	var shown struct{ Fetches, Reconciles int }
	if err := chromedp.Run(ctx, chromedp.Evaluate(`({fetches: window.__fragFetches, reconciles: window.__satelleLive.reconciles})`, &shown)); err != nil {
		t.Fatal(err)
	}
	if n := shown.Reconciles - before.Reconciles; n != 1 {
		t.Fatalf("showing the page ran %d reconcile(s), want exactly 1", n)
	}
	if shown.Fetches == 0 {
		t.Fatal("the reconcile on show fetched no fragment")
	}
}

// visibleRowNow reports whether id's row is rendered and visible right now.
func visibleRowNow(t *testing.T, ctx context.Context, id string) bool {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(jsRowVisible(id), &ok)); err != nil {
		t.Fatal(err)
	}
	return ok
}
