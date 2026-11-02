//go:build integration

// Browser-driven end-to-end tests: they launch the real satelle binary's web
// server and drive it in headless Chrome (chromedp), exercising the actual
// rendered page + JavaScript the user sees — tab switching, inline expand on
// click, live filtering, and realtime updates pushed from a separate CLI
// process. This is the front end under automation, not eyeballing.
//
// Requires a Chrome/Chromium binary; the test skips with a clear message if
// none is found (CI installs one). Part of the `integration` tag.
package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// findBrowser returns a Chrome/Chromium executable path, or "".
func findBrowser() string {
	if p := os.Getenv("SATELLE_CHROME"); p != "" {
		return p
	}
	for _, c := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// serveRepo inits a temp repo, seeds it, starts `satelle serve` on a free port,
// waits until healthy, and returns the project-page base URL + repo path.
// Cleanup stops the server.
//
// The port argument is ignored: hardcoded ports collide with leftover satelle
// processes (healthz succeeds against the WRONG server, rows "never appear").
// Free-port allocation is the isolation mechanism.
//
// serve is always adaptive: the root (/) is the connected-projects landing and
// EVERY repo — including a lone one — is served under its own /<slug>/. So the
// returned base is host+/<slug> (slug == the tempdir basename), making every
// base+"/…" path target this repo's child consistently (project page, detail
// pages, fragments, SSE), with the prefixed <base href> the page itself uses.
func serveRepo(t *testing.T, _ string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	mustRun(t, testBin, repo, "init")
	stubReviewerAccept(t, repo) // baseline gates are active (sty_5b8bd8b2) — keep hermetic
	port := freeListenPort(t)
	// Same isolated SATELLE_HOME as mustRun/init so the home-keyed runtime plane
	// (DB under ~/.satelle/<repo-key>/) matches the CLI (sty_4660bbe1).
	env := append(os.Environ(), "SATELLE_HOME="+isolatedHome(t))
	h := StartServeHealthy(t, testBin, repo, env, testutil.WaitBudget, "--port", port)
	host := h.Base
	// Push-fed mirror: seed [server] endpoint + full snapshot so /r/<slug>/ has data.
	ep := fmt.Sprintf("[server]\nendpoint = %q\n", host)
	_ = os.WriteFile(filepath.Join(repo, ".satelle", "satelle.local.toml"), []byte(ep), 0o644)
	seedWorkspaceAdd(t, testBin, repo, host)
	return host + "/r/" + filepath.Base(repo), repo
}

// newChrome returns a chromedp context (and overall timeout) for the suite.
func newChrome(t *testing.T) context.Context {
	t.Helper()
	browser := findBrowser()
	if browser == "" {
		t.Skip("no Chrome/Chromium found (set SATELLE_CHROME or install google-chrome); skipping browser e2e")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browser),
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		// Chrome 132+ removed the legacy headless mode chromedp defaults to;
		// the new mode is required or the connection hangs.
		chromedp.Flag("headless", "new"),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	// Bounds the whole test, whose individual waits each have testutil.WaitBudget,
	// so it must hold several of them.
	ctx, cancelTimeout := context.WithTimeout(ctx, 4*time.Minute)
	t.Cleanup(cancelTimeout)
	// Launch Chrome here so the first test step does not pay the (speed-dependent)
	// browser start-up out of its own wait budget.
	if err := chromedp.Run(ctx); err != nil {
		t.Fatalf("start chrome: %v", err)
	}
	return ctx
}

// browserStep runs actions under their own budget (2×testutil.WaitBudget) so one
// stuck Navigate/WaitVisible cannot consume the whole-test context, and the
// failure names the step. It retries once, only when the attempt hit its
// deadline (a stuck page load on a starved core): an assertion or selector error
// fails immediately, so a real rendering failure is never retried into a pass.
// The root cause of the observed deadline (sty_05017a43, 1/10 on one loaded
// core) is unconfirmed; a seeding race (page loaded before the pushed snapshot
// landed) is still a candidate the retry would mask, so the step label matters.
func browserStep(t *testing.T, ctx context.Context, label string, actions ...chromedp.Action) {
	t.Helper()
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		stepCtx, cancel := context.WithTimeout(ctx, 2*testutil.WaitBudget)
		err = chromedp.Run(stepCtx, actions...)
		timedOut := stepCtx.Err() != nil && ctx.Err() == nil
		cancel()
		if err == nil {
			return
		}
		if !timedOut {
			break
		}
		t.Logf("browser step %q hit its %s budget (attempt %d): %v", label, 2*testutil.WaitBudget, attempt+1, err)
	}
	t.Fatalf("browser step %q: %v", label, err)
}

func TestBrowserProjectPageInteractions(t *testing.T) {
	// Port 8801 is unusable on some hosts (EADDRINUSE with no LISTEN socket);
	// keep browser e2e ports in a free high range.
	base, repo := serveRepo(t, "8830")

	// Seed: one open story, one done story (so the default status:open filter is
	// observable), one task, and an authored doc.
	openID := createStory(t, repo, "Keep Me Open", "")
	doneID := createStory(t, repo, "Already Done", "done")
	mustRun(t, testBin, repo, "task", "create", "--title", "A task to do")
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "documents", "guide.md"), []byte("# Guide\n\nhello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed an on-disk route so the Workflow panel has a row: embedded defaults are
	// not listed (sty_94da9ac9), so a fresh repo's panel would otherwise be empty.
	// It declares in_progress because progress_column_lights drives a story there:
	// a route may not skip a step, so a backlog→done-only lane would refuse the
	// very transition the light is meant to appear after.
	writeSpineFixture(t, repo, "", "", "",
		"in_progress|executor|||",
		"done|||satelle-story-done-review|reviewer")
	mustRun(t, testBin, repo, "reindex")
	workspaceAddIfConfigured(t, repo)
	_ = doneID

	ctx := newChrome(t)
	// Wait on a signal that the page loaded AND app.js initialized (it sets
	// aria-selected on the active tab) — not on a specific row, which the
	// default status:open filter may have hidden.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="stories"][aria-selected="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	t.Run("default_filter_hides_terminal", func(t *testing.T) {
		// The done story must be hidden under the default status:active filter (it
		// hides terminal rows); the non-terminal story visible.
		if visibleRow(t, ctx, openID) != true {
			t.Errorf("active story %s should be visible by default", openID)
		}
		if visibleRow(t, ctx, doneID) != false {
			t.Errorf("done story %s should be hidden by default (status:active)", doneID)
		}
		// A default status:active chip is rendered.
		if !hasChip(t, ctx, "stories", "status:active") {
			t.Error("expected default status:active chip")
		}
		// The default sort is surfaced the same way: an order:updated chip.
		if !hasChip(t, ctx, "stories", "order:updated") {
			t.Error("expected default order:updated chip")
		}
	})

	t.Run("status_all_reveals_terminal", func(t *testing.T) {
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "status:all")); err != nil {
			t.Fatal(err)
		}
		if !waitCond(t, ctx, jsRowVisible(doneID), 3*time.Second) {
			t.Errorf("status:all should reveal the terminal story %s", doneID)
		}
		if !hasChip(t, ctx, "stories", "status:all") {
			t.Error("expected a status:all chip")
		}
		// Reset to the default for following subtests.
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "")); err != nil {
			t.Fatal(err)
		}
		waitCond(t, ctx, jsRowVisible(openID), 3*time.Second)
	})

	t.Run("clear_all_resets_filters", func(t *testing.T) {
		sel := `#panel-stories .chips .fchip-clear`
		// Absent when the filter input is empty (nothing to clear).
		if !waitCond(t, ctx, "!document.querySelector('"+sel+"')", 3*time.Second) {
			t.Error("clear-all should be absent on an empty filter input")
		}
		// An explicit filter makes the clear-all control appear.
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "status:all")); err != nil {
			t.Fatal(err)
		}
		if !waitCond(t, ctx, "!!document.querySelector('"+sel+"')", 3*time.Second) {
			t.Error("clear-all should appear when an explicit filter is set")
		}
		// Clicking it empties the input and returns to defaults (control gone again).
		clickJS(t, ctx, sel)
		if !waitCond(t, ctx, `document.querySelector('#panel-stories .filterbar input').value === ''`, 3*time.Second) {
			t.Error("clear-all should empty the filter input")
		}
		if !waitCond(t, ctx, "!document.querySelector('"+sel+"')", 3*time.Second) {
			t.Error("clear-all should disappear after clearing (back to defaults)")
		}
		waitCond(t, ctx, jsRowVisible(openID), 3*time.Second)
	})

	t.Run("progress_column_stages", func(t *testing.T) {
		// A fresh open story (still at its initial state, no transitions) shows NO
		// progress stage — the initial state is not a completed stage.
		stage := fmt.Sprintf(`document.querySelector('#panel-stories tr.row[data-expand-url$="%s"] .col-reviews .stage-chip')`, openID)
		if !waitCond(t, ctx, "!"+stage, 3*time.Second) {
			t.Error("a fresh open story should have no progress stage (no phantom current stage)")
		}
		// After a REAL transition, a stage appears — pushed live to the page over
		// the realtime bus. (The coded estimate gate enforces OOTB — record one.)
		mustRun(t, testBin, repo, "story", "estimate", openID, "--time", "10m")
		mustRun(t, testBin, repo, "story", "set", openID, "--status", "in_progress")
		workspaceAddIfConfigured(t, repo)
		if !waitCond(t, ctx, "!!"+stage, 8*time.Second) {
			t.Error("a transitioned story should show a progress stage, pushed live")
		}
		// The STATUS cell holds the status badge alone: gate outcomes read from
		// PROGRESS, never a second line under the status.
		row := fmt.Sprintf(`document.querySelector('#panel-stories tr.row[data-expand-url$="%s"]')`, openID)
		if !waitCond(t, ctx, "!"+row+".querySelector('.gate-badge')", 3*time.Second) {
			t.Error("a story row must carry no .gate-badge")
		}
		if !waitCond(t, ctx, row+".children[2].children.length === 1 && "+row+".children[2].firstElementChild.classList.contains('badge')", 3*time.Second) {
			t.Error("the STATUS cell should hold only the status badge")
		}
		// Counts are told apart by colour alone — the chip carries no tick or cross glyph.
		if !waitCond(t, ctx, "!/[✓✗]/.test("+stage+".textContent)", 3*time.Second) {
			t.Error("a progress stage chip should show no ✓ or ✗ glyph")
		}
	})

	t.Run("tab_switching", func(t *testing.T) {
		clickJS(t, ctx, `.tab[data-panel="tasks"]`)
		if !waitCond(t, ctx, `getComputedStyle(document.querySelector('#panel-tasks')).display === 'block'`, 5*time.Second) {
			t.Fatal("tasks panel did not become visible after clicking its tab")
		}
		var hash, storiesDisplay string
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`location.hash`, &hash),
			chromedp.Evaluate(`getComputedStyle(document.querySelector('#panel-stories')).display`, &storiesDisplay),
		); err != nil {
			t.Fatal(err)
		}
		if hash != "#tasks" {
			t.Errorf("hash = %q, want #tasks", hash)
		}
		if storiesDisplay != "none" {
			t.Errorf("stories panel display = %q, want none while tasks active", storiesDisplay)
		}
		// Documents tab shows the indexed doc card.
		clickJS(t, ctx, `.tab[data-panel="docs"]`)
		if !waitCond(t, ctx, `!!document.querySelector('#panel-docs .doc') && getComputedStyle(document.querySelector('#panel-docs')).display === 'block'`, 5*time.Second) {
			t.Error("documents panel/card not visible after clicking its tab")
		}
		// Workflow tab lists the on-disk workflow and expands to its ROUTE —
		// read-only (sty_085e1a5a).
		clickJS(t, ctx, `.tab[data-panel="workflow"]`)
		if !waitCond(t, ctx, `!!document.querySelector('#panel-workflow tr.row[data-expand-url^="fragment/workflow/"]') && getComputedStyle(document.querySelector('#panel-workflow')).display === 'block'`, 5*time.Second) {
			t.Error("workflow panel/row not visible after clicking its tab")
		}
		// The workflow table carries an Updated column (the Applies-to column was
		// replaced; scope/applies_to render as inline tag chips in the Name cell).
		var hasUpdated, hasAppliesCol bool
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`[...document.querySelectorAll('#panel-workflow thead th')].some(t=>t.textContent.trim()==='Updated')`, &hasUpdated),
			chromedp.Evaluate(`[...document.querySelectorAll('#panel-workflow thead th')].some(t=>t.textContent.trim()==='Applies to')`, &hasAppliesCol),
		); err != nil {
			t.Fatal(err)
		}
		if !hasUpdated || hasAppliesCol {
			t.Errorf("workflow table headers wrong: hasUpdated=%v hasAppliesCol=%v", hasUpdated, hasAppliesCol)
		}
		clickJS(t, ctx, `#panel-workflow tr.row[data-expand-url^="fragment/workflow/"]`)
		if !waitCond(t, ctx, `(function(){var e=document.querySelector('#panel-workflow tr.expansion .expbody');return !!e && e.textContent.includes('Route') && !!document.querySelector('#panel-workflow .route .route-step');})()`, 5*time.Second) {
			t.Error("workflow route did not appear on row click")
		}
		// Each step names its entry gates (or says it has none) — and no diagram.
		if !waitCond(t, ctx, `!!document.querySelector('#panel-workflow .route-step .route-gates') && !document.querySelector('#panel-workflow svg.wf-diagram')`, 5*time.Second) {
			t.Error("workflow route did not render its gate rows, or still renders a diagram")
		}
		// Back to stories for the remaining checks.
		clickJS(t, ctx, `.tab[data-panel="stories"]`)
		if !waitCond(t, ctx, `getComputedStyle(document.querySelector('#panel-stories')).display === 'block'`, 5*time.Second) {
			t.Fatal("could not return to stories tab")
		}
	})

	t.Run("expand_on_click", func(t *testing.T) {
		// Click the open story's row → an inline expansion with its ledger
		// timeline appears.
		rowSel := fmt.Sprintf(`#panel-stories tr.row[data-expand-url$="%s"]`, openID)
		clickJS(t, ctx, rowSel)
		if !waitCond(t, ctx, `(function(){var e=document.querySelector('#panel-stories tr.expansion .expbody');return !!e && e.textContent.includes('Timeline');})()`, 5*time.Second) {
			t.Fatal("inline expansion with timeline did not appear on row click")
		}
		// Click again → collapses (expansion removed).
		clickJS(t, ctx, rowSel)
		if !waitCond(t, ctx, `!document.querySelector('#panel-stories tr.expansion')`, 5*time.Second) {
			t.Error("row did not collapse on second click")
		}
	})

	t.Run("live_filter", func(t *testing.T) {
		// Type status:done → only the done story shows.
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "status:done")); err != nil {
			t.Fatalf("set filter: %v", err)
		}
		if !waitCond(t, ctx, jsRowVisible(doneID), 3*time.Second) {
			t.Error("done story should be visible under status:done")
		}
		if visibleRow(t, ctx, openID) != false {
			t.Error("open story should be hidden under status:done")
		}
		if !hasChip(t, ctx, "stories", "status:done") {
			t.Error("expected status:done chip")
		}
		// Clear the filter; the default status:open returns.
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "")); err != nil {
			t.Fatal(err)
		}
		if !waitCond(t, ctx, jsRowVisible(openID), 3*time.Second) {
			t.Error("open story should be visible again after clearing the filter")
		}
	})

	t.Run("realtime_update_no_reload", func(t *testing.T) {
		// Mark the page so we can prove no full reload happened.
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload = true`, nil)); err != nil {
			t.Fatal(err)
		}
		// Mutate from a SEPARATE process (the CLI), as a user would.
		newID := createStory(t, repo, "Pushed Live RT", "")

		// The open page must show the new row within the wait budget, with no reload.
		deadline := time.Now().Add(testutil.WaitBudget)
		seen := false
		for time.Now().Before(deadline) {
			var present bool
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				fmt.Sprintf(`[...document.querySelectorAll('#panel-stories .row')].some(r => r.getAttribute('data-expand-url')||''.includes('%s')) || document.body.innerHTML.includes('Pushed Live RT')`, newID),
				&present)); err != nil {
				t.Fatal(err)
			}
			if present {
				seen = true
				break
			}
			time.Sleep(300 * time.Millisecond) // poll tick
		}
		if !seen {
			t.Fatalf("new story did not appear on the open page via realtime within %s", testutil.WaitBudget)
		}
		var noReload bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload === true`, &noReload)); err != nil {
			t.Fatal(err)
		}
		if !noReload {
			t.Error("page appears to have reloaded — realtime should update in place")
		}
	})
}

// TestBrowserTagChipFiltering drives the click-a-tag-chip path: clicking a tag
// chip on a row adds its token to the panel filter (not expand the row), and
// clicking the same chip again is a deduped no-op.
func TestBrowserTagChipFiltering(t *testing.T) {
	base, repo := serveRepo(t, "8809")
	mustRun(t, testBin, repo, "story", "create", "--title", "Tagged Story", "--tags", "demo")
	mustRun(t, testBin, repo, "story", "create", "--title", "Untagged Story")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	chipSel := `#panel-stories tr.row .tagchip[data-filter="tags:demo"]`
	if !waitCond(t, ctx, fmt.Sprintf(`!!document.querySelector('%s')`, chipSel), 5*time.Second) {
		t.Fatal("demo tag chip did not render")
	}

	// Click the chip → the filter input gains the token and the matching chip
	// shows; the row must NOT have expanded.
	clickJS(t, ctx, chipSel)
	if !waitCond(t, ctx,
		`document.querySelector('#panel-stories .filterbar input').value.trim() === 'tags:demo'`,
		3*time.Second) {
		t.Error("clicking the tag chip should set the filter input to tags:demo")
	}
	if !hasChip(t, ctx, "stories", "tags:demo") {
		t.Error("expected a tags:demo removable chip after clicking the tag")
	}
	if c := countExpansions(t, ctx); c != 0 {
		t.Errorf("clicking a tag chip must not expand a row; got %d expansions", c)
	}

	// Click the chip again → deduped: still a single token, no duplication.
	clickJS(t, ctx, chipSel)
	if !waitCond(t, ctx,
		`document.querySelector('#panel-stories .filterbar input').value.trim() === 'tags:demo'`,
		3*time.Second) {
		t.Error("clicking the chip again should be a deduped no-op (no duplicate token)")
	}
}

// TestBrowserTimelineFieldToggle drives the real client-side wiring behind the
// per-viewer Timeline-fields control (sty_43d228e4): unchecking a field on the
// Settings page must actually HIDE the matching chip on a story's timeline (via
// localStorage + the hide-<type> class app.js stamps on load), while the other
// chips stay. A telemetry_event supplies the outcome + tokens chips without
// depending on reviewer internals.
func TestBrowserTimelineFieldToggle(t *testing.T) {
	// 8815 is reserved on some WSL hosts (bind EADDRINUSE with no LISTEN socket).
	base, repo := serveRepo(t, "8846")
	id := createStory(t, repo, "Telemetry Story", "")
	mustRun(t, testBin, repo, "story", "log", id, "--kind", "step-quality",
		"--data", "outcome=smooth", "--data", "tokens_total=2000", "--data", "duration_ms=2400")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)

	// The story timeline renders the chips; tokens visible by default (all fields on).
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/story/"+id),
		chromedp.WaitVisible(`ol.timeline .chip-tokens`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("timeline chips did not render: %v", err)
	}
	if !waitCond(t, ctx, `!!document.querySelector('ol.timeline .chip-tokens') && document.querySelector('ol.timeline .chip-tokens').offsetParent!==null`, 3*time.Second) {
		t.Fatal("the tokens chip should be visible by default")
	}

	// Uncheck 'Tokens' on the Settings page (the approved control location).
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/settings"),
		chromedp.WaitVisible(`input[data-tlfield="tokens"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("settings tokens checkbox missing: %v", err)
	}
	clickJS(t, ctx, `input[data-tlfield="tokens"]`) // starts checked → unchecks, writes localStorage

	// Back on the story page: the tokens chip is now hidden; the outcome chip stays.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/story/"+id),
		chromedp.WaitVisible(`ol.timeline .chip-outcome`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reload story: %v", err)
	}
	if !waitCond(t, ctx, `(function(){var c=document.querySelector('ol.timeline .chip-tokens');return !c || c.offsetParent===null;})()`, 3*time.Second) {
		t.Error("after unchecking Tokens, the tokens chip must be hidden on the timeline")
	}
	if !waitCond(t, ctx, `!!document.querySelector('ol.timeline .chip-outcome') && document.querySelector('ol.timeline .chip-outcome').offsetParent!==null`, 3*time.Second) {
		t.Error("the outcome chip must stay visible (only Tokens was unchecked)")
	}
}

// TestBrowserStatusBadgesOutlined asserts the badge restyle (sty_970dbef3) at the
// computed-style level, in BOTH themes: a status badge is an UPPERCASE, OUTLINED
// pill (a real border + matching coloured text, not the old filled light pill), the
// backlog and done badges carry DISTINCT hues, and the backlog text stays legible in
// dark mode (the per-status hue subsuming the earlier sty_173e49a7 dark-only fix).
func TestBrowserStatusBadgesOutlined(t *testing.T) {
	base, repo := serveRepo(t, "8812")
	createStory(t, repo, "Backlog Item", "") // defaults to backlog
	createStory(t, repo, "Finished Item", "done")

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		// status:all so the terminal 'done' row is visible alongside backlog.
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
		setInput(`#panel-stories .filterbar input`, "status:all"),
		chromedp.WaitVisible(`#panel-stories .badge.s-backlog`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories .badge.s-done`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	read := func(sel, prop string) string {
		var v string
		js := fmt.Sprintf(`getComputedStyle(document.querySelector('%s')).%s`, sel, prop)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("read %s.%s: %v", sel, prop, err)
		}
		return v
	}
	setTheme := func(mode string) {
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.documentElement.setAttribute('data-theme','%s')`, mode), nil)); err != nil {
			t.Fatalf("set theme %s: %v", mode, err)
		}
	}

	for _, mode := range []string{"light", "dark"} {
		setTheme(mode)
		// Outlined: a real border whose colour matches the text (transparent-ish fill).
		if w := read(`#panel-stories .badge.s-backlog`, "borderTopWidth"); w == "0px" || w == "" {
			t.Errorf("[%s] backlog badge should have a visible border (outlined pill); got %q", mode, w)
		}
		// Uppercase.
		if tt := read(`#panel-stories .badge.s-backlog`, "textTransform"); tt != "uppercase" {
			t.Errorf("[%s] backlog badge should be uppercase; got %q", mode, tt)
		}
		// Subtle treatment (sty_aed93a00): a visible TINTED border, but the text is
		// softened toward the foreground rather than the raw saturated hue — so the
		// border colour no longer equals the text colour.
		bc := read(`#panel-stories .badge.s-backlog`, "borderTopColor")
		if bc == "" || strings.HasPrefix(bc, "rgba(0, 0, 0, 0") {
			t.Errorf("[%s] backlog badge should keep a visible tinted border; got %q", mode, bc)
		}
		tc := read(`#panel-stories .badge.s-backlog`, "color")
		if tc == bc {
			t.Errorf("[%s] backlog badge text should be softened toward the foreground, not the raw border hue (both %q)", mode, tc)
		}
		// backlog (#2ecc71) and done (#16a34a) stay distinct hues.
		if doneC := read(`#panel-stories .badge.s-done`, "color"); doneC == tc {
			t.Errorf("[%s] backlog and done badges must be distinct colours; both %q", mode, tc)
		}
		// Legible on BOTH themes: the softened text keeps enough channel sum to read
		// against the panel (the readability goal — bright mint on white was poor).
		r, g, b := parseRGB(t, tc)
		if mode == "dark" && r+g+b < 200 {
			t.Errorf("[dark] backlog badge text too dark to read; got %q", tc)
		}
		if mode == "light" && r+g+b > 690 {
			t.Errorf("[light] backlog badge text too washed out to read on the light panel; got %q", tc)
		}
	}
}

// TestBrowserSubtleTagChips asserts the satellites subtle-tile alignment (sty_aed93a00):
// a kv tag chip is ONE uniform translucent chip — the key carries NO opaque fill (the
// loud filled key is gone), the chip background is a single translucent tint, and the
// ':' separator is preserved — while filter chips stay legible in dark theme (the old
// hardcoded light-only colours were invisible there).
func TestBrowserSubtleTagChips(t *testing.T) {
	base, repo := serveRepo(t, "8818")
	// A category (→ category:feature kv chip) plus an epic kv tag.
	mustRun(t, testBin, repo, "story", "create", "--title", "Subtle", "--category", "feature", "--tags", "epic:issue-intake")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories tr.row .tagchip.kv .k`, chromedp.ByQuery),
		// surface a removable .fchip in the filter strip
		setInput(`#panel-stories .filterbar input`, "status:all"),
		chromedp.WaitVisible(`#panel-stories .chips .fchip`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	read := func(sel, prop string) string {
		var v string
		js := fmt.Sprintf(`getComputedStyle(document.querySelector('%s')).%s`, sel, prop)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("read %s.%s: %v", sel, prop, err)
		}
		return v
	}
	setTheme := func(mode string) {
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.documentElement.setAttribute('data-theme','%s')`, mode), nil)); err != nil {
			t.Fatalf("set theme %s: %v", mode, err)
		}
	}

	kv := `#panel-stories tr.row .tagchip.kv`
	// AC1: the key carries NO opaque fill (the loud filled key is gone).
	if bg := read(kv+` .k`, "backgroundColor"); bg != "rgba(0, 0, 0, 0)" && bg != "transparent" {
		t.Errorf("kv key should have no fill (uniform subtle chip); got %q", bg)
	}
	// AC1: the chip itself is a single translucent tint (alpha < 1), not a solid block.
	if chipBg := read(kv, "backgroundColor"); !translucent(chipBg) {
		t.Errorf("kv chip background should be a translucent tint; got %q", chipBg)
	}
	// AC1: the key:value ':' separator is preserved (via .k::after).
	var colon string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`getComputedStyle(document.querySelector('%s .k'), '::after').content`, kv), &colon)); err != nil {
		t.Fatalf("read ::after content: %v", err)
	}
	if !strings.Contains(colon, ":") {
		t.Errorf("kv chip should keep the ':' separator; got %q", colon)
	}

	// AC3: filter chips read in dark — the chip text must be light enough to see on
	// the dark panel (the old hardcoded #374151 was invisible there).
	for _, mode := range []string{"light", "dark"} {
		setTheme(mode)
		fc := read(`#panel-stories .chips .fchip`, "color")
		r, g, b := parseRGB(t, fc)
		if mode == "dark" && r+g+b < 300 {
			t.Errorf("[dark] filter chip text too dark to read on the dark panel; got %q", fc)
		}
	}
}

// TestBrowserPageWidth asserts the wider layout (sty_aed93a00): the content wrap
// takes ~80% of the viewport (≈10% side margins) but is capped at a reasonable
// max-width so a super-wide viewport never goes full-bleed.
func TestBrowserPageWidth(t *testing.T) {
	base, repo := serveRepo(t, "8819")
	createStory(t, repo, "Width", "")

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.wrap`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}
	var maxW string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.querySelector('.wrap')).maxWidth`, &maxW)); err != nil {
		t.Fatalf("read maxWidth: %v", err)
	}
	if maxW != "1600px" {
		t.Errorf("wrap should cap at a reasonable max-width (1600px); got %q", maxW)
	}
	var wrapW, innerW float64
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('.wrap').getBoundingClientRect().width`, &wrapW),
		chromedp.Evaluate(`window.innerWidth`, &innerW),
	); err != nil {
		t.Fatalf("measure widths: %v", err)
	}
	// Margins exist: the wrap is narrower than the viewport (not full-bleed).
	if wrapW >= innerW {
		t.Errorf("wrap (%.0f) should leave side margins, narrower than the viewport (%.0f)", wrapW, innerW)
	}
	// Below the cap, the wrap sits near 80% of the viewport (≈10% margins each side).
	if innerW*0.8 <= 1600 {
		if ratio := wrapW / innerW; ratio < 0.7 || ratio > 0.9 {
			t.Errorf("wrap should be ~80%% of a sub-cap viewport; got ratio %.2f (wrap %.0f / inner %.0f)", ratio, wrapW, innerW)
		}
	}
}

// TestBrowserSquaredEdges asserts the industrial squared-edge restyle (sty_10c6c5b0)
// at the computed-style level: the four label families the story enumerates — tag
// chips (.tagchip), filter chips (.fchip), status badges (.badge), and the backlog
// count pill (.tab .n-backlog) — all report a 0px border-radius, in BOTH themes,
// while a non-label control (.theme-toggle) keeps its rounded corner (the scope
// guard: only chips/badges/pills square off, not buttons/panels/cards/inputs).
func TestBrowserSquaredEdges(t *testing.T) {
	base, repo := serveRepo(t, "8817")
	// A tagged backlog story renders a tag chip AND a backlog badge AND the
	// stories-tab backlog count pill, all on the default view.
	mustRun(t, testBin, repo, "story", "create", "--title", "Tagged Backlog", "--tags", "demo")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories tr.row .tagchip`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tab[data-panel="stories"] .n-backlog`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories .badge.s-backlog`, chromedp.ByQuery),
		// A filter token surfaces a removable .fchip in the stories filterbar.
		setInput(`#panel-stories .filterbar input`, "status:all tags:demo"),
		chromedp.WaitVisible(`#panel-stories .chips .fchip`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	read := func(sel, prop string) string {
		var v string
		js := fmt.Sprintf(`getComputedStyle(document.querySelector('%s')).%s`, sel, prop)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("read %s.%s: %v", sel, prop, err)
		}
		return v
	}
	setTheme := func(mode string) {
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.documentElement.setAttribute('data-theme','%s')`, mode), nil)); err != nil {
			t.Fatalf("set theme %s: %v", mode, err)
		}
	}

	// The four label families the story squares off.
	squared := []string{
		`#panel-stories tr.row .tagchip`,
		`#panel-stories .chips .fchip`,
		`#panel-stories .badge.s-backlog`,
		`.tab[data-panel="stories"] .n-backlog`,
	}
	for _, mode := range []string{"light", "dark"} {
		setTheme(mode)
		for _, sel := range squared {
			if r := read(sel, "borderTopLeftRadius"); r != "0px" {
				t.Errorf("[%s] %s should have a squared (0px) corner; got %q", mode, sel, r)
			}
		}
		// Scope guard: a non-label control (the theme toggle) keeps its radius.
		if r := read(`.theme-toggle`, "borderTopLeftRadius"); r == "0px" || r == "" {
			t.Errorf("[%s] .theme-toggle is not a label and must keep its rounded corner; got %q", mode, r)
		}
	}
}

// parseRGB pulls the r,g,b channels (0–255) out of a CSS computed-colour string,
// handling both the legacy "rgb(...)"/"rgba(...)" forms and Chrome's "color(srgb
// R G B [/ A])" output for color-mix() (where R,G,B are 0–1 floats).
func parseRGB(t *testing.T, s string) (int, int, int) {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(s, "rgb(%d, %d, %d)", &r, &g, &b); err == nil {
		return r, g, b
	}
	if _, err := fmt.Sscanf(s, "rgba(%d, %d, %d", &r, &g, &b); err == nil {
		return r, g, b
	}
	var rf, gf, bf float64
	if _, err := fmt.Sscanf(s, "color(srgb %g %g %g", &rf, &gf, &bf); err == nil {
		return int(rf*255 + 0.5), int(gf*255 + 0.5), int(bf*255 + 0.5)
	}
	t.Fatalf("parseRGB %q: unrecognised colour format", s)
	return 0, 0, 0
}

// translucent reports whether a computed colour string carries alpha < 1, across
// both Chrome's "color(srgb R G B / A)" (color-mix) and "rgba(r, g, b, a)" forms.
func translucent(s string) bool {
	if i := strings.Index(s, "/"); i >= 0 {
		var a float64
		if _, err := fmt.Sscanf(s[i:], "/ %g", &a); err == nil {
			return a < 1
		}
	}
	if strings.HasPrefix(s, "rgba(") {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(s, "rgba("), ")"), ",")
		if len(parts) == 4 {
			var a float64
			if _, err := fmt.Sscanf(strings.TrimSpace(parts[3]), "%g", &a); err == nil {
				return a < 1
			}
		}
	}
	return false
}

// TestBrowserTimelineDotsByOutcome asserts at the computed-colour level that the
// story-detail timeline dots are coloured by event outcome (sty_f19d2ec4): a
// review_reject dot is the fail red and a review_accept dot the pass green, matching
// the process-light palette, while a neutral event keeps the default accent dot.
// Checked on the standalone detail page (one of the two surfaces the shared
// template feeds), in both light and dark themes.
func TestBrowserTimelineDotsByOutcome(t *testing.T) {
	// Avoid 8813 — blackholed / stuck on some hosts after prior bind races.
	base, repo := serveRepo(t, "8853")
	id := createStory(t, repo, "Timeline Story", "")
	// Seed outcome-bearing + neutral ledger events on this story.
	mustRun(t, testBin, repo, "ledger", "append", "--kind", "review_reject", "--actor", "reviewer", "--story", id, "--body", "rejected a->b")
	mustRun(t, testBin, repo, "ledger", "append", "--kind", "review_accept", "--actor", "reviewer", "--story", id, "--body", "accepted a->b")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/story/"+id),
		chromedp.WaitVisible(`ol.timeline li`, chromedp.ByQuery),
		chromedp.WaitVisible(`ol.timeline li.tl-fail`, chromedp.ByQuery),
		chromedp.WaitVisible(`ol.timeline li.tl-pass`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load detail page: %v", err)
	}

	dotColour := func(sel string) string {
		var v string
		js := fmt.Sprintf(`getComputedStyle(document.querySelector('%s'), '::before').backgroundColor`, sel)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("read %s ::before: %v", sel, err)
		}
		return v
	}
	setTheme := func(mode string) {
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.documentElement.setAttribute('data-theme','%s')`, mode), nil)); err != nil {
			t.Fatalf("set theme %s: %v", mode, err)
		}
	}

	for _, mode := range []string{"light", "dark"} {
		setTheme(mode)
		if c := dotColour(`ol.timeline li.tl-fail`); c != "rgb(231, 76, 60)" {
			t.Errorf("[%s] review_reject dot should be fail red #e74c3c (rgb(231, 76, 60)); got %q", mode, c)
		}
		if c := dotColour(`ol.timeline li.tl-pass`); c != "rgb(46, 204, 113)" {
			t.Errorf("[%s] review_accept dot should be pass green #2ecc71 (rgb(46, 204, 113)); got %q", mode, c)
		}
		// A neutral event (the story_created li, un-classed) keeps the accent dot —
		// neither the fail red nor the pass green.
		if c := dotColour(`ol.timeline li:not(.tl-pass):not(.tl-fail)`); c == "rgb(231, 76, 60)" || c == "rgb(46, 204, 113)" {
			t.Errorf("[%s] a neutral event dot must not be an outcome colour; got %q", mode, c)
		}
	}
}

// TestBrowserBacklogBadgeLiveOnRefetch asserts the Stories tab 'N backlog' badge
// stays live across a realtime (SSE-driven) refetch (sty_af09a484): creating a
// backlog story from a SEPARATE CLI process bumps the badge without a reload, and
// the badge is removed when the live backlog count reaches zero.
func TestBrowserBacklogBadgeLiveOnRefetch(t *testing.T) {
	base, repo := serveRepo(t, "8814")
	id1 := createStory(t, repo, "First Backlog", "") // defaults to backlog

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="stories"] .n-backlog`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}
	badgeText := `(document.querySelector('.tab[data-panel="stories"] .n-backlog')||{}).textContent`
	noBadge := `!document.querySelector('.tab[data-panel="stories"] .n-backlog')`

	// Server-rendered initial value.
	if !waitCond(t, ctx, badgeText+" === '1 backlog'", 3*time.Second) {
		t.Error("initial badge should read '1 backlog'")
	}
	// Sentinel to prove no full reload happens across the live update.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__t4 = true`, nil)); err != nil {
		t.Fatal(err)
	}

	// A SECOND backlog story created by a separate CLI process must bump the badge
	// live (the bug: it stayed frozen at the page-load value).
	id2 := createStory(t, repo, "Second Backlog", "")
	if !waitCond(t, ctx, badgeText+" === '2 backlog'", 6*time.Second) {
		t.Error("badge should update live to '2 backlog' after a backlog story is created via CLI")
	}

	// And it must disappear when the live backlog count drops to zero.
	// One performing story at a time (sty_c7149f8a / sty_a614a0ea): engage id1,
	// park it to free the seat, then engage id2 so both leave backlog.
	mustRun(t, testBin, repo, "story", "estimate", id1, "--time", "10m")
	mustRun(t, testBin, repo, "story", "estimate", id2, "--time", "10m")
	mustRun(t, testBin, repo, "story", "set", id1, "--status", "in_progress")
	workspaceAddIfConfigured(t, repo)
	mustRun(t, testBin, repo, "story", "set", id1, "--status", "blocked")
	workspaceAddIfConfigured(t, repo)
	mustRun(t, testBin, repo, "story", "set", id2, "--status", "in_progress")
	workspaceAddIfConfigured(t, repo)
	if !waitCond(t, ctx, noBadge, 6*time.Second) {
		t.Error("badge should be removed when the live backlog count reaches 0")
	}

	// No full-page reload occurred — the update was the SSE refetch path.
	var noReload bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__t4 === true`, &noReload)); err != nil {
		t.Fatal(err)
	}
	if !noReload {
		t.Error("the badge update must come from the live refetch, not a page reload")
	}
}

// TestBrowserMarkTracksConnection validates the ◐ brand mark's fused signals
// end-to-end (sty_cd2fe2f3): its TITLE carries the "up …" snapshot, and its COLOUR
// tracks the LIVE SSE connection — the mark is accent-green (no 'sse-down' class)
// once the /events stream is open, and the retired uptime pill is gone.
func TestBrowserMarkTracksConnection(t *testing.T) {
	// 8815 is reserved on some WSL hosts (bind EADDRINUSE with no LISTEN socket).
	base, _ := serveRepo(t, "8847")
	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.brand-mark`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}
	// The retired uptime pill is gone.
	var pill bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('.uptime')`, &pill)); err != nil {
		t.Fatal(err)
	}
	if pill {
		t.Error("retired uptime pill is still rendered")
	}
	// The mark's title carries the "up …" snapshot + the connection note.
	var title string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.brand-mark').title`, &title)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "up ") || !strings.Contains(title, "at page load") || !strings.Contains(title, "live-update connection") {
		t.Errorf("brand-mark title should fold in the uptime snapshot + connection note; got %q", title)
	}
	// Once the SSE stream opens, the mark is connected (no 'sse-down' red class).
	if !waitCond(t, ctx, `!document.querySelector('.brand-mark').classList.contains('sse-down')`, 5*time.Second) {
		t.Error("brand mark should be connected (no sse-down class) once the live SSE stream opens")
	}
}

// TestBrowserMarkSoftRedOnDisconnect drives AC3 of sty_2faa7dd4 in the real browser:
// on a live /events disconnect only the ◐ MARK turns a MUTED soft-red (not the
// saturated fail red) while the "satelle" WORDMARK stays ink, and it returns to the
// accent on reconnect. The disconnect is driven through the shared stream's real
// teardown path (sty_1ea8202b): the leader's drop() closes its EventSource through
// the error path and adds .sse-down; reopen() opens it again and clears the class.
func TestBrowserMarkSoftRedOnDisconnect(t *testing.T) {
	base, _ := serveRepo(t, "8822")
	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.brand-mark svg`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}

	markColour := func() (int, int, int) {
		var v string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.querySelector('.brand-mark svg')).color`, &v)); err != nil {
			t.Fatalf("read mark colour: %v", err)
		}
		return parseRGB(t, v)
	}
	wordColour := func() string {
		var v string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.querySelector('.brand-word')).color`, &v)); err != nil {
			t.Fatalf("read wordmark colour: %v", err)
		}
		return v
	}

	// Connected: the stream opens (no sse-down) and the ◐ is the accent green
	// (green-dominant channel), distinct from the ink wordmark.
	if !waitCond(t, ctx, `!document.querySelector('.brand-mark').classList.contains('sse-down')`, 5*time.Second) {
		t.Fatal("mark should start connected (no sse-down) once the SSE stream opens")
	}
	cr, cg, cb := markColour()
	if !(cg > cr && cg >= cb) {
		t.Errorf("connected ◐ should be the accent green (green-dominant); got rgb(%d,%d,%d)", cr, cg, cb)
	}
	wordConnected := wordColour()
	// The wordmark ink differs from the accent ◐ — the whole brand is not one colour.
	var svgConnected string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`getComputedStyle(document.querySelector('.brand-mark svg')).color`, &svgConnected)); err != nil {
		t.Fatal(err)
	}
	if wordConnected == svgConnected {
		t.Errorf("wordmark and ◐ should be different colours (ink vs accent); both %q", wordConnected)
	}

	// Disconnect via the REAL teardown path: this lone page leads the shared
	// stream, and drop() closes it through the error path, adding .sse-down.
	if !waitCond(t, ctx, `window.__satelleLive.leader && window.__satelleLive.open`, 5*time.Second) {
		t.Fatal("a lone page should lead the shared /events stream")
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__satelleLive.drop()`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitCond(t, ctx, `document.querySelector('.brand-mark').classList.contains('sse-down')`, 5*time.Second) {
		t.Fatal("dropping the shared stream should add sse-down (the /events stream is torn down)")
	}

	// The ◐ is now a MUTED red — red-dominant, but softer than the saturated fail
	// red (#e74c3c = rgb(231,76,60)): its red channel is visibly lower.
	dr, dg, db := markColour()
	if !(dr > dg && dr > db) {
		t.Errorf("disconnected ◐ should be red-dominant (soft red); got rgb(%d,%d,%d)", dr, dg, db)
	}
	if dr >= 231 {
		t.Errorf("disconnected ◐ should be a MUTED red, not the saturated fail red (#e74c3c); got rgb(%d,%d,%d)", dr, dg, db)
	}
	// The wordmark stays ink — the disconnect never reddens the word.
	if wd := wordColour(); wd != wordConnected {
		t.Errorf("wordmark should stay ink on disconnect (was %q, now %q)", wordConnected, wd)
	}

	// Reconnect: reopen() → the leader reopens the stream and clears sse-down; the
	// ◐ returns to the accent green.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__satelleLive.reopen()`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitCond(t, ctx, `!document.querySelector('.brand-mark').classList.contains('sse-down')`, 6*time.Second) {
		t.Error("reopening the shared stream should clear sse-down")
	}
	rr, rg, rb := markColour()
	if !(rg > rr && rg >= rb) {
		t.Errorf("reconnected ◐ should return to the accent green; got rgb(%d,%d,%d)", rr, rg, rb)
	}
}

// TestBrowserWorkflowRoute replaces the two diagram interaction tests
// (sty_19b2107a's hover/correlate, sty_677c604c's pan/zoom/toggle) with the
// surface that took their place: the route is SERVER-rendered — the ordered
// steps with their performer and entry gates, and the off-route exits — so
// there is no diagram to enhance and no JS interaction to drive. The
// assertions are the old ones inverted: nothing named wf-diagram, no graph
// library, and the route legible with scripting or without (sty_085e1a5a).
func TestBrowserWorkflowRoute(t *testing.T) {
	base, repo := serveRepo(t, "8816")
	// A gated spine plus a cancel fan — the shape the retired diagram existed
	// to draw, and the one the route now states in words. The fan is SYNTHESISED:
	// the route declares only the cancel state.
	writeSpineFixture(t, repo, "", "cancelled @satelle-story-cancel-review", "",
		"in_progress|executor|code||",
		"commit|executor|||",
		"rev|||satelle-story-done-review|reviewer",
		"done||||")
	mustRun(t, testBin, repo, "reindex")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load page: %v", err)
	}
	clickJS(t, ctx, `.tab[data-panel="workflow"]`)
	// Target the authored declaration of done specifically — init seeds the
	// default workflow set, so the first row is no longer the one under test.
	if !waitCond(t, ctx, `!!document.querySelector('#panel-workflow tr.row[data-expand-url="fragment/workflow/done"]')`, 5*time.Second) {
		t.Fatal("workflow row did not list")
	}
	clickJS(t, ctx, `#panel-workflow tr.row[data-expand-url="fragment/workflow/done"]`)
	if !waitCond(t, ctx, `!!document.querySelector('#panel-workflow .route .route-step[data-state="in_progress"]')`, 5*time.Second) {
		t.Fatal("route did not render with per-step identifiers")
	}

	// No graph library — and none needed, since nothing is drawn.
	var scriptSrcs []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('script[src]')].map(s=>s.getAttribute('src'))`, &scriptSrcs)); err != nil {
		t.Fatal(err)
	}
	for _, s := range scriptSrcs {
		if strings.Contains(strings.ToLower(s), "mermaid") || strings.Contains(strings.ToLower(s), "d3") || strings.Contains(strings.ToLower(s), "cytoscape") {
			t.Errorf("a graph library was loaded (%q) — the route is server-rendered text", s)
		}
	}

	// The route is complete without any script running: the spine in order, each
	// step's performer and rubric, the reviewer gating entry, and the cancel
	// destination as an off-route exit rather than a step.
	var got string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		var body=document.querySelector('#panel-workflow tr.expansion .expbody');
		return JSON.stringify({
			order: [...body.querySelectorAll('.route-step')].map(function(li){return li.dataset.state;}),
			text: body.textContent,
			diagram: !!body.querySelector('svg.wf-diagram')
		});
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `["backlog","in_progress","commit","rev","done"]`) {
		t.Errorf("route steps should read in workflow order; got %s", got)
	}
	for _, want := range []string{"executor", "@skill:code", "satelle-story-done-review", "exits (off-route)", "cancelled", "satelle-story-cancel-review"} {
		if !strings.Contains(got, want) {
			t.Errorf("route missing %q; got %s", want, got)
		}
	}
	if strings.Contains(got, `"diagram":true`) {
		t.Error("the workflow panel must render no diagram")
	}
	// cancelled is an EXIT, never a step on the route.
	if strings.Contains(got, `"cancelled","`) || strings.Contains(got, `,"cancelled"]`) {
		t.Errorf("cancelled must be an exit, not a route step; got %s", got)
	}
}

// countExpansions returns how many inline expansion rows are open in the stories
// panel.
func countExpansions(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelectorAll('#panel-stories tr.expansion').length`, &n)); err != nil {
		t.Fatalf("countExpansions: %v", err)
	}
	return n
}

// TestBrowserUserPath walks a realistic session: a user opens the project page
// and expands a story while the agent (a separate CLI process) progresses that
// story — asserting the open expansion's timeline grows LIVE without collapsing,
// then breadcrumb-navigates to the detail page (which also live-updates) and
// back, and sorts with order:. This is the "live, navigable" requirement under
// automation.
func TestBrowserUserPath(t *testing.T) {
	base, repo := serveRepo(t, "8803")
	slugPath := "/r/" + filepath.Base(repo) + "/" // the project page's own path/<base href>
	// Two open stories so order: is observable; the first gets progressed live.
	betaID := createStory(t, repo, "Beta story", "")
	alphaID := createStory(t, repo, "Alpha story", "")
	_ = alphaID

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="stories"][aria-selected="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#panel-stories table.panel-table`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load: %v", err)
	}

	t.Run("expand_then_live_progress", func(t *testing.T) {
		rowSel := fmt.Sprintf(`#panel-stories tr.row[data-expand-url$="%s"]`, betaID)
		clickJS(t, ctx, rowSel)
		if !waitCond(t, ctx, `(function(){var e=document.querySelector('#panel-stories tr.expansion .expbody');return !!e && e.textContent.includes('story_created');})()`, 5*time.Second) {
			t.Fatal("expansion timeline did not show story_created")
		}
		before := evalInt(t, ctx, `document.querySelectorAll('#panel-stories tr.expansion .timeline li').length`)

		// The agent progresses the story from ANOTHER process. (The coded
		// estimate gate enforces OOTB — record one first.)
		mustRun(t, testBin, repo, "story", "estimate", betaID, "--time", "10m")
		mustRun(t, testBin, repo, "story", "set", betaID, "--status", "in_progress")
		workspaceAddIfConfigured(t, repo)

		// The OPEN expansion must gain the transition event live, without collapsing.
		grew := waitCond(t, ctx, fmt.Sprintf(
			`(function(){var e=document.querySelector('#panel-stories tr.expansion .timeline');return !!e && e.querySelectorAll('li').length > %d && e.textContent.includes('status_transition');})()`, before),
			8*time.Second)
		if !grew {
			t.Fatal("open expansion timeline did not grow live on status change")
		}
		var expanded bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`document.querySelector('tr.row[data-expand-url$="%s"]').getAttribute('aria-expanded')==='true'`, betaID), &expanded)); err != nil {
			t.Fatal(err)
		}
		if !expanded {
			t.Error("row collapsed during live refresh — expansion should persist")
		}
	})

	t.Run("order_sort", func(t *testing.T) {
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "order:title")); err != nil {
			t.Fatal(err)
		}
		ok := waitCond(t, ctx, `(function(){
			var titles=[...document.querySelectorAll('#panel-stories tr.row')].filter(r=>r.style.display!=='none').map(r=>r.dataset.title);
			return titles.length>=2 && titles[0]==='alpha story' && titles.indexOf('beta story')>0;
		})()`, 3*time.Second)
		if !ok {
			t.Error("order:title did not sort Alpha before Beta")
		}
		if !hasChip(t, ctx, "stories", "order:title") {
			t.Error("expected order:title chip")
		}
		if err := chromedp.Run(ctx, setInput(`#panel-stories .filterbar input`, "")); err != nil {
			t.Fatal(err)
		}
		waitCond(t, ctx, jsRowVisible(betaID), 3*time.Second)
	})

	t.Run("id_copy_does_not_toggle_or_navigate", func(t *testing.T) {
		// Clicking the id copies it (shows "copied ✓" feedback) and must NOT toggle
		// the row or navigate the page — stop-propagation.
		before := evalInt(t, ctx, `document.querySelectorAll('#panel-stories tr.expansion').length`)
		clickJS(t, ctx, fmt.Sprintf(`#panel-stories tr.row[data-expand-url$="%s"] .id-copy`, betaID))
		if !waitCond(t, ctx, `[...document.querySelectorAll('#panel-stories .id-copy')].some(function(e){return e.classList.contains('copied')||e.textContent.indexOf('copied')>=0;})`, 3*time.Second) {
			t.Error("id-copy did not show 'copied' feedback")
		}
		after := evalInt(t, ctx, `document.querySelectorAll('#panel-stories tr.expansion').length`)
		if after != before {
			t.Errorf("clicking the id changed expansion count %d→%d (should not toggle the row)", before, after)
		}
		var path string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`location.pathname`, &path)); err != nil {
			t.Fatal(err)
		}
		if path != slugPath {
			t.Errorf("clicking the id navigated to %q (should stay on the project page %q)", path, slugPath)
		}
	})

	t.Run("breadcrumb_to_detail_live_and_back", func(t *testing.T) {
		// The id is a copy control now; navigation moved to the panel's Open story
		// link. Expand the row, then click it.
		clickJS(t, ctx, fmt.Sprintf(`#panel-stories tr.row[data-expand-url$="%s"]`, betaID))
		if !waitCond(t, ctx, `!!document.querySelector('#panel-stories tr.expansion a.open-story')`, 5*time.Second) {
			t.Fatal("Open story link not present after expanding the row")
		}
		clickJS(t, ctx, `#panel-stories tr.expansion a.open-story`)
		if !waitCond(t, ctx, `!!document.querySelector('#detail-live') && !!document.querySelector('.crumbs')`, 8*time.Second) {
			t.Fatal("did not land on the detail page with a breadcrumb")
		}
		// The standalone detail page hides its own "Open story →" self-link — it is
		// present on the expanded project-page card (clicked just above) but redundant
		// here.
		var hasSelfLink bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('#detail-live a.open-story')`, &hasSelfLink)); err != nil {
			t.Fatal(err)
		}
		if hasSelfLink {
			t.Error("standalone detail page should not render its own Open story self-link")
		}
		beforeLi := evalInt(t, ctx, `document.querySelectorAll('#detail-live .timeline li').length`)
		// Mutate the story from ANOTHER process — a priority change records a ledger
		// row without depending on a particular workflow's edges — and the open
		// detail page must gain it live.
		mustRun(t, testBin, repo, "story", "set", betaID, "--priority", "high")
		workspaceAddIfConfigured(t, repo)
		if !waitCond(t, ctx, fmt.Sprintf(`document.querySelectorAll('#detail-live .timeline li').length > %d`, beforeLi), 8*time.Second) {
			t.Error("detail page timeline did not live-update")
		}
		clickJS(t, ctx, fmt.Sprintf(`.crumbs a[href=%q]`, slugPath))
		if !waitCond(t, ctx, `!!document.querySelector('.tabs') && !!document.querySelector('#panel-stories')`, 8*time.Second) {
			t.Fatal("breadcrumb 'project' did not return to the project page")
		}
	})
}

// --- chromedp helpers ---

// evalInt evaluates a JS expression to an int.
func evalInt(t *testing.T, ctx context.Context, js string) int {
	t.Helper()
	var n int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &n)); err != nil {
		t.Fatalf("evalInt: %v", err)
	}
	return n
}

// clickJS clicks an element via element.click() — robust against chromedp's
// position/visibility heuristics for elements in just-shown panels.
// TestBrowserTaskPanelNativeRuns drives headless Chrome to prove the tasks panel
// is task-native (sty_30a917f8): expanding a task row shows its runs (executions)
// with a status badge, and a live execution-status change refreshes the open
// expansion without a reload.
func TestBrowserTaskPanelNativeRuns(t *testing.T) {
	// Unique port — 8803 is used by TestBrowserUserPath.
	base, repo := serveRepo(t, "8852")

	taskID := extractID(mustRun(t, testBin, repo, "task", "create",
		"--title", "Runnable task", "--body", "ACTION: do it. VERIFICATION: done."), "tsk_")
	if taskID == "" {
		t.Fatal("no task id")
	}
	exeID := extractID(mustRun(t, testBin, repo, "execution", "create",
		"--parent", taskID, "--title", "run 1", "--status", "in_progress"), "exe_")
	if exeID == "" {
		t.Fatal("no execution id")
	}
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="stories"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// Switch to the tasks panel and expand the task row.
	clickJS(t, ctx, `.tab[data-panel="tasks"]`)
	if !waitCond(t, ctx, `getComputedStyle(document.querySelector('#panel-tasks')).display === 'block'`, 5*time.Second) {
		t.Fatal("tasks panel did not show")
	}
	rowSel := fmt.Sprintf(`#panel-tasks tr.row[data-expand-url$="%s"]`, taskID)
	// Wait for the task row to appear (panel list is async after tab switch).
	if !waitCond(t, ctx, fmt.Sprintf(`!!document.querySelector(%q)`, rowSel), 8*time.Second) {
		t.Fatalf("task row not shown for %s", taskID)
	}
	clickJS(t, ctx, rowSel)

	// The expansion is task-native: a Runs section listing the in-progress run.
	runShown := fmt.Sprintf(`(function(){var e=document.querySelector('#panel-tasks tr.expansion .expbody');return !!e && e.textContent.includes('Runs') && e.textContent.includes('%s') && !!e.querySelector('.badge.s-in_progress');})()`, exeID)
	if !waitCond(t, ctx, runShown, 5*time.Second) {
		t.Fatal("task expansion did not render the native run list with the in-progress run")
	}

	// Live update: close the run from a separate process; the open expansion must
	// refresh to show it done, with no reload.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload = true`, nil)); err != nil {
		t.Fatal(err)
	}
	mustRun(t, testBin, repo, "execution", "set", exeID, "--status", "done")
	workspaceAddIfConfigured(t, repo)
	doneShown := `(function(){var e=document.querySelector('#panel-tasks tr.expansion .expbody');return !!e && !!e.querySelector('.badge.s-done');})()`
	if !waitCond(t, ctx, doneShown, 10*time.Second) {
		t.Error("open task expansion did not refresh to show the run done on a live status change")
	}
	var reloaded bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload !== true`, &reloaded)); err != nil {
		t.Fatal(err)
	}
	if reloaded {
		t.Error("a full page reload happened; the update should be live")
	}
}

func clickJS(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	js := fmt.Sprintf(`(function(){var e=document.querySelector(%q);if(e)e.click();return !!e;})()`, sel)
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &ok)); err != nil {
		t.Fatalf("clickJS %s: %v", sel, err)
	}
	if !ok {
		t.Fatalf("clickJS: element not found: %s", sel)
	}
}

// waitCond polls a JS boolean expression until true or the timeout elapses. The
// timeout is a floor, never a ceiling a slow machine can hit: the wait is on a
// page the test does not control, it returns the moment the expression holds, and
// no caller asks for less than testutil.WaitBudget (sty_05017a43).
func waitCond(t *testing.T, ctx context.Context, js string, timeout time.Duration) bool {
	t.Helper()
	if timeout < testutil.WaitBudget {
		timeout = testutil.WaitBudget
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &ok)); err == nil && ok {
			return true
		}
		time.Sleep(100 * time.Millisecond) // poll tick
	}
	return false
}

// jsRowVisible is a JS expression: is the row for id visible (not display:none)?
func jsRowVisible(id string) string {
	return fmt.Sprintf(`(function(){var r=document.querySelector('tr.row[data-expand-url$="%s"]');return !!r && getComputedStyle(r).display!=='none';})()`, id)
}

// createStory creates a story via the CLI and returns its id. When the repo is
// wired for push-fed serve ([server] endpoint in satelle.local.toml), re-seeds
// so the mirror reflects the new item without a manual workspace add in every test.
func createStory(t *testing.T, repo, title, status string) string {
	t.Helper()
	args := []string{"story", "create", "--title", title, "--body", "the goal", "--acceptance", "1. it works"}
	if status != "" {
		args = append(args, "--status", status)
	}
	out := mustRun(t, testBin, repo, args...)
	id := extractID(out, "sty_")
	workspaceAddIfConfigured(t, repo)
	return id
}

// workspaceAddIfConfigured runs `satelle workspace add` when [server] endpoint is set.
// Suite-wide SATELLE_SERVER_ENDPOINT=none requires the URL override (sty_5aa08259).
func workspaceAddIfConfigured(t *testing.T, repo string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, ".satelle", "satelle.local.toml"))
	if err != nil || !strings.Contains(string(b), "endpoint") {
		return
	}
	// endpoint = "http://..."
	ep := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "endpoint") {
			if i := strings.Index(line, "\""); i >= 0 {
				rest := line[i+1:]
				if j := strings.Index(rest, "\""); j >= 0 {
					ep = rest[:j]
				}
			}
		}
	}
	if ep == "" {
		return
	}
	seedWorkspaceAdd(t, testBin, repo, ep)
}

// visibleRow reports whether the story/task row for id is visible (not
// display:none) in the DOM.
func visibleRow(t *testing.T, ctx context.Context, id string) bool {
	t.Helper()
	var vis bool
	js := fmt.Sprintf(`(function(){
		var r = document.querySelector('tr.row[data-expand-url$="%s"]');
		if (!r) return false;
		return getComputedStyle(r).display !== 'none';
	})()`, id)
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &vis)); err != nil {
		t.Fatalf("visibleRow(%s): %v", id, err)
	}
	return vis
}

// TestBrowserDocRendersMarkdown opens a document from the Documents tab and
// asserts its markdown was rendered to HTML server-side (a heading element
// exists), not shown as raw text.
func TestBrowserDocRendersMarkdown(t *testing.T) {
	base, _ := serveRepo(t, "8807")
	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="docs"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load: %v", err)
	}
	clickJS(t, ctx, `.tab[data-panel="docs"]`)
	if !waitCond(t, ctx, `!!document.querySelector('#panel-docs a.doc[href^="doc/"]')`, 5*time.Second) {
		t.Fatal("no clickable doc card in the Documents tab")
	}
	var href string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#panel-docs a.doc[href^="doc/"]').getAttribute('href')`, &href)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"+href),
		chromedp.WaitVisible(`article.doc-article`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("doc page %q: %v", href, err)
	}
	var hasHeading bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`!!document.querySelector('article.doc-article h1, article.doc-article h2, article.doc-article h3')`, &hasHeading)); err != nil {
		t.Fatal(err)
	}
	if !hasHeading {
		t.Error("doc viewer did not render markdown headings — body shown as raw text?")
	}
}

// TestBrowserTimelineDocLink (sty_49666ca9): attach a named doc → timeline
// carries a real link for it; click from the inline expand lands on the
// standalone story page with that doc open/reviewable; ← returns to the
// project stories tab with that row expanded.
func TestBrowserTimelineDocLink(t *testing.T) {
	base, repo := serveRepo(t, "8860")
	id := createStory(t, repo, "Timeline doc link story", "")
	mustRun(t, testBin, repo, "story", "attach", id, "--name", "plan", "--type", "plan",
		"--body", "# Plan\n\n- clickable from timeline\n")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.tab[data-panel="stories"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	rowSel := fmt.Sprintf(`#panel-stories tr.row[data-expand-url$="%s"]`, id)
	if !waitCond(t, ctx, fmt.Sprintf(`!!document.querySelector(%q)`, rowSel), 8*time.Second) {
		t.Fatalf("story row not shown for %s", id)
	}
	clickJS(t, ctx, rowSel)
	linkSel := `#panel-stories tr.expansion .timeline .ev-doc a`
	if !waitCond(t, ctx, fmt.Sprintf(`!!document.querySelector(%q)`, linkSel), 8*time.Second) {
		t.Fatal("timeline doc link not present after expanding the row")
	}

	clickJS(t, ctx, linkSel)
	if !waitCond(t, ctx, `!!document.querySelector('#detail-live') && location.pathname.indexOf('/story/') !== -1`, 8*time.Second) {
		t.Fatal("timeline doc link did not land on the standalone story page")
	}
	var path string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.pathname`, &path)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/story/"+id) {
		t.Errorf("pathname = %q, want …/story/%s", path, id)
	}
	if !waitCond(t, ctx, `!!document.querySelector('#detail-live .timeline')`, 5*time.Second) {
		t.Fatal("standalone page missing the full timeline")
	}
	// Hash-open: the clicked doc is expanded with rendered markdown.
	if !waitCond(t, ctx, `(function(){var d=document.querySelector('.doc-list .doc-item[open]#doc-plan .doc-article');return !!d && !!d.querySelector('h1');})()`, 5*time.Second) {
		t.Error("clicked document is not open/reviewable (rendered markdown) on the standalone page")
	}

	clickJS(t, ctx, `.crumbs .back-link`)
	if !waitCond(t, ctx, `!!document.querySelector('#panel-stories') && getComputedStyle(document.querySelector('#panel-stories')).display !== 'none'`, 8*time.Second) {
		t.Fatal("← did not return to the project stories tab")
	}
	expanded := fmt.Sprintf(`(function(){var r=document.querySelector('tr.row[data-expand-url$="%s"]');return !!r && r.getAttribute('aria-expanded')==='true' && !!r.nextElementSibling && r.nextElementSibling.classList.contains('expansion');})()`, id)
	if !waitCond(t, ctx, expanded, 8*time.Second) {
		t.Error("← did not restore the expanded story row")
	}
}

// TestBrowserStoryDocList attaches a document to a story and asserts it renders
// as a collapsible LIST entry before the Timeline (sty_1a239b4d) — collapsed by
// default (a list, not a wall of text), expanding on click to reveal the rendered
// markdown, with no legacy tabstrip.
func TestBrowserStoryDocList(t *testing.T) {
	base, repo := serveRepo(t, "8808")
	id := createStory(t, repo, "Doc list story", "")
	mustRun(t, testBin, repo, "story", "attach", id, "--name", "plan", "--type", "plan",
		"--body", "# Plan\n\n- step one\n- step two")
	workspaceAddIfConfigured(t, repo)

	ctx := newChrome(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/story/"+id),
		chromedp.WaitVisible(`.doc-list .doc-item > summary`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("story document list entry not rendered: %v", err)
	}
	// The legacy tabstrip is gone; the body is collapsed by default; and the
	// documents list sits before the timeline.
	var checks struct {
		NoTabs    bool
		Collapsed bool
		BeforeTL  bool
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		var tabs = document.querySelector('.doc-tabstrip, .doc-tab, .doc-pane');
		var det = document.querySelector('.doc-list .doc-item');
		var collapsed = !!det && !det.open;
		var list = document.querySelector('.doc-list');
		var tl = [...document.querySelectorAll('h4')].find(h => h.textContent.trim()==='Timeline');
		var before = !!list && !!tl && (list.compareDocumentPosition(tl) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
		return {NoTabs: !tabs, Collapsed: collapsed, BeforeTL: before};
	})()`, &checks)); err != nil {
		t.Fatal(err)
	}
	if !checks.NoTabs {
		t.Error("legacy doc tabstrip/panes must be gone")
	}
	if !checks.Collapsed {
		t.Error("document body must be collapsed by default (a list, not a wall of text)")
	}
	if !checks.BeforeTL {
		t.Error("the documents list must sit before the Timeline")
	}
	// Clicking the summary reveals the rendered markdown.
	clickJS(t, ctx, `.doc-list .doc-item > summary`)
	var rendered bool
	if !waitCond(t, ctx, `(function(){var d=document.querySelector('.doc-list .doc-item[open] .doc-article');return !!d && !!d.querySelector('h1') && !!d.querySelector('li');})()`, 5*time.Second) {
		rendered = false
	} else {
		rendered = true
	}
	if !rendered {
		t.Error("expanding a document did not render its markdown (heading + list)")
	}
}

// hasChip reports whether the named panel shows a filter chip with the label.
func hasChip(t *testing.T, ctx context.Context, panel, label string) bool {
	t.Helper()
	var has bool
	js := fmt.Sprintf(`[...document.querySelectorAll('#panel-%s .chips .fchip')].some(c => c.textContent.replace('×','').trim() === '%s')`, panel, label)
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &has)); err != nil {
		t.Fatalf("hasChip: %v", err)
	}
	return has
}

// TestBrowserSharedTopbar asserts the one shared navbar band renders identically
// across all four named surfaces — project page, story detail, /workspace, and
// /settings — with the Satelle Design System order (◐ satelle mark leads LEFT,
// account controls right-aligned, theme toggle rightmost), the DS ☾ glyph (not ◐),
// and the retired uptime pill gone. Real served binary via chromedp: source AND
// visual (getBoundingClientRect) order, so a CSS regression that reorders the flex
// row is caught, not just markup presence (sty_cd2fe2f3).
func TestBrowserSharedTopbar(t *testing.T) {
	// High free range: 8806 is unusable on some hosts (same class as 8801).
	base, repo := serveRepo(t, "8840")
	id := createStory(t, repo, "Topbar story", "")
	ctx := newChrome(t)

	// One evaluate per surface: element presence, source order (compareDocumentPosition),
	// visual left-to-right order (bounding rects), the toggle glyph, and no uptime pill.
	// sty_eea989dd: push-fed MirrorRO without identity email omits the .signin strip
	// (no bare "mirror" mode pill). Brand-mark + theme-toggle are always required;
	// .account/.signin is optional and, when present, sits between them.
	const probe = `(function(){
		var bm = document.querySelector('header.topbar .brand-mark');
		var acct = document.querySelector('header.topbar .account, header.topbar .signin');
		var tt = document.querySelector('header.topbar #theme-toggle');
		if (!bm || !tt) return {OK:false};
		var srcOrder, visOrder;
		var b = bm.getBoundingClientRect(), c = tt.getBoundingClientRect();
		if (acct) {
			srcOrder = (bm.compareDocumentPosition(acct) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0 &&
			           (acct.compareDocumentPosition(tt) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
			var a = acct.getBoundingClientRect();
			visOrder = b.left < a.left && a.left < c.left;
		} else {
			srcOrder = (bm.compareDocumentPosition(tt) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
			visOrder = b.left < c.left;
		}
		return {OK:true, SrcOrder:srcOrder, VisOrder:visOrder,
		        Glyph: tt.textContent.trim(), Uptime: !!document.querySelector('.uptime')};
	})()`

	type navState struct {
		OK, SrcOrder, VisOrder, Uptime bool
		Glyph                          string
	}
	// Surfaces under the project base (/r/slug/) plus host-level workspace.
	host := strings.TrimSuffix(base, "/r/"+filepath.Base(repo))
	for _, s := range []struct{ name, path string }{
		{"project", base + "/"},
		{"detail", base + "/story/" + id},
		{"workspace", host + "/"},
		{"settings", base + "/settings"},
	} {
		var st navState
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.path),
			chromedp.WaitVisible(`header.topbar .brand-mark`, chromedp.ByQuery),
			chromedp.WaitVisible(`header.topbar #theme-toggle`, chromedp.ByQuery),
			chromedp.Evaluate(probe, &st),
		); err != nil {
			t.Fatalf("%s (%s): %v", s.name, s.path, err)
		}
		if !st.OK {
			t.Errorf("%s: navbar missing brand-mark / theme-toggle", s.name)
			continue
		}
		if !st.SrcOrder {
			t.Errorf("%s: source order is not brand-mark → [account?] → theme-toggle", s.name)
		}
		if !st.VisOrder {
			t.Errorf("%s: visual order is not mark-left → [controls] → toggle-rightmost", s.name)
		}
		if st.Glyph != "☾" {
			t.Errorf("%s: theme toggle glyph = %q, want the DS ☾ (never ◐)", s.name, st.Glyph)
		}
		if st.Uptime {
			t.Errorf("%s: retired uptime pill still present", s.name)
		}
	}
}

// setInput sets an input's value and fires an 'input' event (so listeners run).
func setInput(sel, val string) chromedp.Action {
	js := fmt.Sprintf(`(function(){
		var el = document.querySelector(%q);
		el.value = %q;
		el.dispatchEvent(new Event('input', { bubbles: true }));
	})()`, sel, val)
	return chromedp.Evaluate(js, nil)
}

// liveState is one page's window.__satelleLive snapshot: whether it holds an
// /events stream (open, opens), leads the shared stream, runs the per-tab
// fallback, and how many on-reconnect reconciles it has run.
type liveState struct {
	Open, Leader, Fallback bool
	Opens, Reconciles      int
}

func readLive(ctx context.Context) (liveState, error) {
	var s liveState
	err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var l=window.__satelleLive||{};
		return {open:!!l.open,leader:!!l.leader,fallback:!!l.fallback,opens:l.opens||0,reconciles:l.reconciles||0};})()`, &s))
	return s, err
}

// TestBrowserSharedLiveStream drives sty_1ea8202b in headless Chrome: ten
// pages in ONE browser profile share ONE /events stream. The Web Lock leader
// holds it and relays every trigger and its connection state over a
// BroadcastChannel; closing the leader hands the stream to another page within
// a second, and that page reconciles. An eleventh page still loads — the
// browser's ~6-connections-per-host pool is no longer starved by streams.
func TestBrowserSharedLiveStream(t *testing.T) {
	base, repo := serveRepo(t, "8851")
	browser := newChrome(t)

	// Every tab comes from the one browser so all share the profile, the lock
	// and the channel.
	newTab := func() (context.Context, context.CancelFunc) {
		tab, cancel := chromedp.NewContext(browser)
		t.Cleanup(cancel)
		if err := chromedp.Run(tab); err != nil {
			t.Fatalf("open tab: %v", err)
		}
		return tab, cancel
	}
	snapshot := func(tabs []context.Context) []liveState {
		out := make([]liveState, len(tabs))
		for i, tab := range tabs {
			s, err := readLive(tab)
			if err != nil {
				t.Fatalf("read tab %d live state: %v", i, err)
			}
			out[i] = s
		}
		return out
	}
	const markDown = `document.querySelector('.brand-mark').classList.contains('sse-down')`

	const pages = 10
	tabs := make([]context.Context, pages)
	cancels := make([]context.CancelFunc, pages)
	for i := range tabs {
		tabs[i], cancels[i] = newTab()
		if err := chromedp.Run(tabs[i],
			chromedp.Navigate(base+"/#stories"),
			chromedp.WaitVisible(`.brand-mark`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load page %d: %v", i, err)
		}
	}

	// AC1: exactly one leader, and it holds the one open stream between them.
	leader := -1
	var states []liveState
	for deadline := time.Now().Add(testutil.WaitBudget); ; {
		states = snapshot(tabs)
		leaders, open := 0, 0
		for i, s := range states {
			if s.Leader {
				leaders++
				leader = i
			}
			if s.Open {
				open++
			}
		}
		if leaders == 1 && open == 1 && states[leader].Open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("want exactly one leader holding the one open /events stream across %d pages; got %d leaders, %d open: %+v", pages, leaders, open, states)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i, s := range states {
		if s.Fallback {
			t.Fatalf("page %d fell back to a per-tab stream with Web Locks and BroadcastChannel available: %+v", i, s)
		}
	}

	// AC5 (open half): every page's mark shows the shared stream connected.
	for i, tab := range tabs {
		if !waitCond(t, tab, `!`+markDown, 5*time.Second) {
			t.Fatalf("page %d mark should be connected (no sse-down) while the leader's stream is open", i)
		}
	}

	// AC2: a live trigger reaches every page through the relay.
	createStory(t, repo, "SharedStreamStory", "")
	for i, tab := range tabs {
		wctx, cancel := context.WithTimeout(tab, testutil.WaitBudget)
		err := chromedp.Run(wctx, chromedp.WaitVisible(`#panel-stories .row[data-title="sharedstreamstory"]`, chromedp.ByQuery))
		cancel()
		if err != nil {
			t.Fatalf("page %d (leader %d) never showed the story created after load: %v", i, leader, err)
		}
	}

	// AC5 (down half): the leader's stream errors → every page's mark goes red;
	// reopening it clears every mark.
	if err := chromedp.Run(tabs[leader], chromedp.Evaluate(`window.__satelleLive.drop()`, nil)); err != nil {
		t.Fatalf("drop the leader's stream: %v", err)
	}
	for i, tab := range tabs {
		if !waitCond(t, tab, markDown, 5*time.Second) {
			t.Fatalf("page %d mark should show sse-down after the leader's stream errors", i)
		}
	}
	if err := chromedp.Run(tabs[leader], chromedp.Evaluate(`window.__satelleLive.reopen()`, nil)); err != nil {
		t.Fatalf("reopen the leader's stream: %v", err)
	}
	for i, tab := range tabs {
		if !waitCond(t, tab, `!`+markDown, 5*time.Second) {
			t.Fatalf("page %d mark should clear sse-down once the leader's stream reopens", i)
		}
	}

	// AC4: with the ten pages open, an eleventh page load returns and renders.
	extra, _ := newTab()
	lctx, lcancel := context.WithTimeout(extra, 5*time.Second)
	err := chromedp.Run(lctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`.brand-mark`, chromedp.ByQuery),
	)
	lcancel()
	if err != nil {
		t.Fatalf("an 11th page load with %d pages open did not render within 5s: %v", pages, err)
	}

	// AC3: close the leader → another page takes the lock, reopens /events and
	// reconciles within one second. Timed here, not by waitCond (whose budget
	// floor would hide a slow handover).
	var rest []context.Context
	for i, tab := range tabs {
		if i != leader {
			rest = append(rest, tab)
		}
	}
	rest = append(rest, extra)
	before := snapshot(rest)
	cancels[leader]()
	t0 := time.Now()
	deadline := t0.Add(time.Second)
	for {
		after := snapshot(rest)
		took := false
		for i, s := range after {
			if s.Leader && s.Open {
				took = true
				if s.Reconciles > before[i].Reconciles {
					t.Logf("handover in %v: page took the lead, reopened /events and reconciled (%d → %d)", time.Since(t0), before[i].Reconciles, s.Reconciles)
					return
				}
			}
		}
		if time.Now().After(deadline) {
			if took {
				t.Fatalf("a page took the lead within 1s but did not run the on-reconnect reconcile: before %+v after %+v", before, after)
			}
			t.Fatalf("no remaining page took the lead with an open /events stream within 1s of the leader closing: %+v", after)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestBrowserLiveFallback drives sty_1ea8202b AC7: where the browser lacks Web
// Locks or BroadcastChannel, a page holds its own /events stream as before —
// not a leader, flagged as the fallback. Each case removes one API before app.js
// runs, so a feature check that tested only the other one fails that case.
func TestBrowserLiveFallback(t *testing.T) {
	base, _ := serveRepo(t, "8852")
	browser := newChrome(t)
	cases := []struct {
		name, remove, gone string
	}{
		{"no web locks", `delete Navigator.prototype.locks;`, `!('locks' in navigator)`},
		{"no broadcast channel", `delete window.BroadcastChannel;`, `!('BroadcastChannel' in window)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tab, cancel := chromedp.NewContext(browser)
			defer cancel()
			if err := chromedp.Run(tab,
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, err := page.AddScriptToEvaluateOnNewDocument(tc.remove).Do(ctx)
					return err
				}),
				chromedp.Navigate(base+"/#stories"),
				chromedp.WaitVisible(`.brand-mark`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("load page: %v", err)
			}
			var gone bool
			if err := chromedp.Run(tab, chromedp.Evaluate(tc.gone, &gone)); err != nil || !gone {
				t.Fatalf("the API was not removed before app.js ran (%s = %v, err %v)", tc.gone, gone, err)
			}
			if !waitCond(t, tab, `window.__satelleLive.fallback && window.__satelleLive.open && !window.__satelleLive.leader`, 5*time.Second) {
				s, _ := readLive(tab)
				t.Fatalf("want the page's own /events stream with leader:false, fallback:true; got %+v", s)
			}
			if s, err := readLive(tab); err != nil || s.Opens != 1 {
				t.Fatalf("the fallback page should open exactly its own one stream; got %+v (err %v)", s, err)
			}
		})
	}
}
