package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// TestLandingUniqueHrefsOnSlugCollision (sty_57d5ce25 AC2/AC3): legacy
// partitions that share a basename slug still render distinct landing hrefs
// (repo_key fallback) that each resolve to a project page.
func TestLandingUniqueHrefsOnSlugCollision(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.TouchPartition(ctx, "002-aaaa", "002", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TouchPartition(ctx, "002-bbbb", "002", now); err != nil {
		t.Fatal(err)
	}

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("landing status %d", resp.StatusCode)
	}
	body := string(raw)

	re := regexp.MustCompile(`href="/r/([^"]+)/"`)
	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) < 2 {
		t.Fatalf("expected ≥2 project hrefs, got %v\n%s", matches, body)
	}
	seen := map[string]int{}
	for _, m := range matches {
		seen[m[1]]++
	}
	for slug, n := range seen {
		if n > 1 {
			t.Errorf("duplicate landing href /r/%s/ appears %d times", slug, n)
		}
	}
	// Colliding basename should not appear as the sole shared card.
	if seen["002"] > 0 {
		t.Errorf("colliding basename slug still used as href: %v", seen)
	}
	for _, hrefSlug := range []string{"002-aaaa", "002-bbbb"} {
		if seen[hrefSlug] != 1 {
			t.Errorf("want href /r/%s/ once, got %d (map %v)", hrefSlug, seen[hrefSlug], seen)
		}
		pr, err := http.Get(srv.URL + "/r/" + hrefSlug + "/")
		if err != nil {
			t.Fatal(err)
		}
		pr.Body.Close()
		if pr.StatusCode != 200 {
			t.Errorf("GET /r/%s/ status %d", hrefSlug, pr.StatusCode)
		}
	}
}

// TestMirrorTopbarOmitsBareMirrorPill (sty_eea989dd): empty-identity topbar has
// no "mirror" mode chrome; Install/Docs/Projects stay; identity email keeps RO copy.
func TestMirrorTopbarOmitsBareMirrorPill(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.TouchPartition(ctx, "rk-empty", "emptyid", now); err != nil {
		t.Fatal(err)
	}

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	if strings.Contains(body, ">mirror</span>") || strings.Contains(body, `title="push-fed identity"`) {
		t.Errorf("empty-identity landing still has opaque mirror pill:\n%s", body)
	}
	for _, want := range []string{
		">Install</a>", ">Docs</a>", ">Projects</a>", ">Logs</a>",
		"https://satelle.dev/install", "https://satelle.dev/docs",
		`class="theme-toggle"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing missing %q", want)
		}
	}
}

func TestDisplaySlugsUniqueOnCollision(t *testing.T) {
	parts := []mirror.Partition{
		{RepoKey: "a-1", Slug: "app"},
		{RepoKey: "a-2", Slug: "app"},
		{RepoKey: "b-1", Slug: "other"},
		{RepoKey: "c-1", Slug: ""},
	}
	got := displaySlugs(parts)
	if got["a-1"] != "a-1" || got["a-2"] != "a-2" {
		t.Errorf("colliding app slugs = %q / %q, want repo_keys", got["a-1"], got["a-2"])
	}
	if got["b-1"] != "other" {
		t.Errorf("unique slug = %q, want other", got["b-1"])
	}
	if got["c-1"] != "c-1" {
		t.Errorf("empty slug = %q, want repo_key", got["c-1"])
	}
}

// TestAppJSProjectsSoftRefreshNoReload (sty_f968f9db): landing no longer
// location.reload()s on projects SSE — soft-refresh path is present instead.
func TestAppJSProjectsSoftRefreshNoReload(t *testing.T) {
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "refetchProjects") || !strings.Contains(src, "applyProjectsLive") {
		t.Error("app.js missing projects soft-refresh helpers")
	}
	if !strings.Contains(src, "fragment/projects") {
		t.Error("app.js must fetch fragment/projects")
	}
	// Old full-page reload on projects page must be gone.
	if strings.Contains(src, `if (isProjects) { location.reload()`) ||
		strings.Contains(src, `ev.data === "projects" && isProjects) location.reload()`) {
		t.Error("app.js still full-page reloads the projects landing")
	}
}

// TestAppJSMirrorsRelTimeWording is the JS half of the AC7 wording contract
// (sty_226a661e). relPhrase() in app.js and relTime() in page.go re-render the
// SAME element — the server writes the phrase, the client rewrites it on a
// timer — so a divergent edit to one side makes the text visibly re-word on the
// first tick with no time elapsed.
//
// There is no JS runtime in this toolchain, so this is a static fence, not an
// execution test: it asserts the literal strings and thresholds exist on the JS
// side. TestRelTimeBuckets is what actually proves the Go semantics.
func TestAppJSMirrorsRelTimeWording(t *testing.T) {
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"relPhrase", "renderRelTimes", "time.rel-time[datetime]",
		`"just now"`, `"1 min ago"`, `" mins ago"`,
		`"1 hr ago"`, `" hrs ago"`, `"1 day ago"`, `" days ago"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js is missing %s — the client phrasing must mirror relTime() in page.go", want)
		}
	}
	// AC4: a timer, and one that is not gated on visibility.
	if !strings.Contains(src, "initRelTimes") || !strings.Contains(src, "setInterval") {
		t.Error("app.js must tick the freshness phrases on a timer")
	}
	// AC6: a class absent from copyCellCounts silently freezes after the first
	// SSE tick, with no error — the exact trap this column could fall into.
	//
	// Scoped to the FUNCTION BODY, not the whole file, and that is not fussiness:
	// the first version of this check searched all of app.js and passed with the
	// class removed from the array, because the comment above the array still
	// named it. An assertion a nearby comment can satisfy proves nothing.
	body := copyCellCountsBody(t, src)
	if !strings.Contains(body, `"updated-cell"`) {
		t.Errorf("copyCellCounts must carry updated-cell or the column freezes on soft refresh:\n%s", body)
	}
	// sty_be23ae50: soft refresh must leave the Project name cell (and its
	// stretched <a>) alone — only count/updated classes are rewritten.
	if strings.Contains(body, `wi-title`) || strings.Contains(body, `row-link`) {
		t.Errorf("copyCellCounts must not rewrite the project-name cell:\n%s", body)
	}
}

// copyCellCountsBody returns the class list inside copyCellCounts — from the
// function keyword to the end of the forEach it drives.
func copyCellCountsBody(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "function copyCellCounts")
	if start < 0 {
		t.Fatal("copyCellCounts is gone from app.js — the soft refresh no longer copies cells")
	}
	rest := src[start:]
	end := strings.Index(rest, ".forEach(")
	if end < 0 {
		t.Fatalf("copyCellCounts no longer drives a forEach over a class list:\n%s", rest[:min(len(rest), 300)])
	}
	return rest[:end]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestAppJSTickerAddsNoConnection is AC5: the freshness ticker must not have
// become an excuse for a second stream or a poll. HTTP/1.1 caps ~6 connections
// per host, so app.js shares ONE /events stream across every open page
// (sty_1ea8202b): the Web Lock leader holds it and relays it over a
// BroadcastChannel. A timer costs none of that — but only while it stays a timer.
// It also pins sty_1ea8202b AC6: visibility never opens or closes a stream, so
// there is no per-tab path beside the shared one. The one visibility listener
// only runs the deferred reconcile of a page that was hidden (sty_a003a459).
func TestAppJSTickerAddsNoConnection(t *testing.T) {
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if n := strings.Count(src, "new EventSource"); n != 1 {
		t.Errorf("want exactly 1 EventSource (the shared stream's one call site), got %d", n)
	}
	for _, want := range []string{"navigator.locks", "BroadcastChannel"} {
		if !strings.Contains(src, want) {
			t.Errorf("the shared live stream must coordinate pages through %s", want)
		}
	}
	if n := strings.Count(src, "visibilitychange"); n > 1 {
		t.Errorf("want at most 1 visibilitychange listener (the deferred reconcile), got %d", n)
	}
	if _, after, ok := strings.Cut(src, `"visibilitychange"`); ok {
		handler, _, _ := strings.Cut(after, "});")
		for _, stream := range []string{"openStream", "closeStream", "EventSource", ".close()"} {
			if strings.Contains(handler, stream) {
				t.Errorf("the visibility listener touches the stream (%s): no per-tab stream path beside the shared one", stream)
			}
		}
	}
	// The ticker re-renders from the DOM; it must not fetch to learn the time.
	if strings.Contains(src, "renderRelTimes") && strings.Contains(src, "fetch(\"fragment/time") {
		t.Error("the ticker must render from the element's datetime, never by asking the server")
	}
}

func httpGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s status %d", url, resp.StatusCode)
	}
	return string(raw)
}
