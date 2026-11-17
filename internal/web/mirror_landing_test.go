package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestLandingUniqueHrefsOnSlugCollision(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	keyA, keyB := addedProject(t, "002"), addedProject(t, "002")
	if _, err := s.TouchPartition(ctx, keyA, "002", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TouchPartition(ctx, keyB, "002", now); err != nil {
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
	if seen["002"] > 0 {
		t.Errorf("colliding basename slug still used as href: %v", seen)
	}
	for _, hrefSlug := range []string{keyA, keyB} {
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
	if strings.Contains(src, `if (isProjects) { location.reload()`) ||
		strings.Contains(src, `ev.data === "projects" && isProjects) location.reload()`) {
		t.Error("app.js still full-page reloads the projects landing")
	}
}

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
	if !strings.Contains(src, "initRelTimes") || !strings.Contains(src, "setInterval") {
		t.Error("app.js must tick the freshness phrases on a timer")
	}
	body := copyCellCountsBody(t, src)
	if !strings.Contains(body, `"updated-cell"`) {
		t.Errorf("copyCellCounts must carry updated-cell or the column freezes on soft refresh:\n%s", body)
	}
	if strings.Contains(body, `wi-title`) || strings.Contains(body, `row-link`) {
		t.Errorf("copyCellCounts must not rewrite the project-name cell:\n%s", body)
	}
}

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

func TestLandingListsThisMachinesProjects(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	story := func(key, id string) {
		t.Helper()
		if err := s.ReplaceKind(ctx, key, "story", []mirror.ItemRow{{ID: id, Payload: `{"id":"` + id + `","kind":"story","title":"T","status":"backlog"}`}}, now); err != nil {
			t.Fatal(err)
		}
	}

	keyA := addedProject(t, "alpha")
	if _, err := s.TouchPartition(ctx, keyA, "alpha", now); err != nil {
		t.Fatal(err)
	}
	story(keyA, "sto_a")
	dirB := filepath.Join(t.TempDir(), "bravo")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	addPath(t, dirB)

	dirC, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dirC = filepath.Join(dirC, "charlie")
	if err := os.MkdirAll(dirC, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dirC, "init", "-q")
	gittest.Git(t, dirC, "commit", "-q", "--allow-empty", "-m", "init")
	wtC := filepath.Join(filepath.Dir(dirC), "charlie-wt")
	gittest.Git(t, dirC, "worktree", "add", "-q", "-b", "side", wtC)
	keyC := addPath(t, dirC)
	if k := addPath(t, wtC); k != keyC {
		t.Fatalf("the worktree's repo key %s differs from its repo's %s", k, keyC)
	}
	if _, err := s.TouchPartition(ctx, keyC, "charlie", now); err != nil {
		t.Fatal(err)
	}
	story(keyC, "sto_c")

	if _, err := s.TouchPartition(ctx, "stray-0000beef", "stray", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKind(ctx, "stray-0000beef", "identity", []mirror.ItemRow{{ID: "meta", Payload: `{"project_name":"stray","repo_root":"/tmp/stray"}`}}, now); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewMirror(s).Handler)
	t.Cleanup(srv.Close)
	rowRE := regexp.MustCompile(`(?s)<tr class="row" data-slug="([^"]*)">(.*?)</tr>`)
	for _, path := range []string{"/", "/fragment/projects"} {
		body := get(t, srv.URL+path)
		rows := rowRE.FindAllStringSubmatch(body, -1)
		if len(rows) != 4 {
			t.Fatalf("%s: %d project rows, want 4 (alpha, bravo, charlie and its worktree):\n%s", path, len(rows), body)
		}
		ids := map[string]bool{}
		byPath := map[string]string{}
		for _, r := range rows {
			ids[r[1]] = true
			for _, p := range []string{dirB, dirC, wtC} {
				if strings.Contains(r[2], ">"+p+"<") {
					byPath[p] = r[2]
				}
			}
			if strings.Contains(r[2], "alpha") {
				byPath["alpha"] = r[2]
			}
		}
		if len(ids) != 4 {
			t.Errorf("%s: row ids %v are not unique", path, ids)
		}
		if a := byPath["alpha"]; !strings.Contains(a, `href="/r/alpha/"`) || !strings.Contains(a, `<td class="n-stories"><span class="n">1</span>`) {
			t.Errorf("%s: alpha's row has no link or no story count:\n%s", path, a)
		}
		if b := byPath[dirB]; b == "" || strings.Contains(b, "href=") || !strings.Contains(b, ">not sent<") || !strings.Contains(b, ">bravo<") {
			t.Errorf("%s: bravo, added but never pushed, is not listed as not sent with no link:\n%s", path, b)
		}
		for _, p := range []string{dirC, wtC} {
			if c := byPath[p]; !strings.Contains(c, `href="/r/charlie/"`) || !strings.Contains(c, `<td class="n-stories"><span class="n">1</span>`) {
				t.Errorf("%s: %s, which shares charlie's partition, lacks its link or count:\n%s", path, p, c)
			}
		}
		if strings.Contains(body, "stray") {
			t.Errorf("%s: the stray partition of no added project is listed:\n%s", path, body)
		}
	}
	if resp, err := http.Get(srv.URL + "/r/stray-0000beef/"); err != nil || resp.StatusCode != 200 {
		t.Errorf("the stray partition's own page: %v %v, want 200", resp, err)
	}
}
