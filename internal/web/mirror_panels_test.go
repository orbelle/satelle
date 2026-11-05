package web

import (
	"context"
	"encoding/json"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workitem"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMirrorLoadPanelsFromKindsOnly proves sty_400c022b AC2: pageData fields
// assemble from mirror kinds alone (no repo DB).
func TestMirrorLoadPanelsFromKindsOnly(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-panels"
	if _, err := s.TouchPartition(ctx, rk, "demo", now); err != nil {
		t.Fatal(err)
	}

	story := workitem.Item{
		ID: "sty_x", Kind: workitem.KindStory, Title: "Panel Story",
		Status: workitem.StatusBacklog, Category: "feature", Priority: "high",
		UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	task := workitem.Item{
		ID: "tsk_x", Kind: workitem.KindTask, Title: "Panel Task",
		Status: workitem.StatusBacklog, UpdatedAt: now, CreatedAt: now,
	}
	tb, _ := json.Marshal(task)
	doc := map[string]any{
		"name": "toy", "kind": "workflows", "body": "---\nname: toy\napplies_to: [\"*\"]\n---\n",
		"headline": "toy", "mod_time": now, "provenance": "authored", "source": "/x/toy.md",
	}
	db, _ := json.Marshal(doc)
	led := map[string]any{
		"id": "evt_1", "story_id": "sty_x", "kind": "status_transition",
		"created_at": now, "payload": map[string]string{"from": "backlog", "to": "plan"},
	}
	lb, _ := json.Marshal(led)
	seat, _ := json.Marshal(map[string]any{"id": "sty_x", "in_flight": true, "stale": false})
	ident, _ := json.Marshal(mirror.IdentityMeta{
		ProjectName: "demo", RepoRoot: "/tmp/demo", FooterEmail: "op@example.com",
	})

	for _, r := range []struct {
		kind string
		rows []mirror.ItemRow
	}{
		{"story", []mirror.ItemRow{{ID: "sty_x", Payload: string(sb)}}},
		{"task", []mirror.ItemRow{{ID: "tsk_x", Payload: string(tb)}}},
		{"doc", []mirror.ItemRow{{ID: "toy", Payload: string(db)}}},
		{"ledger_event", []mirror.ItemRow{{ID: "evt_1", Payload: string(lb)}}},
		{"seat", []mirror.ItemRow{{ID: "sty_x", Payload: string(seat)}}},
		{"identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}},
	} {
		if err := s.ReplaceKind(ctx, rk, r.kind, r.rows, now); err != nil {
			t.Fatal(err)
		}
	}

	data, id, err := mirrorLoadPanels(ctx, s, rk, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if id.FooterEmail != "op@example.com" {
		t.Errorf("footer email = %q", id.FooterEmail)
	}
	if data.ProjectName != "demo" || data.RepoRoot != "/tmp/demo" {
		t.Errorf("identity fields: name=%q root=%q", data.ProjectName, data.RepoRoot)
	}
	if data.BacklogCount != 1 {
		t.Errorf("BacklogCount = %d, want 1", data.BacklogCount)
	}
	if len(data.Stories) != 1 || data.Stories[0].Title != "Panel Story" {
		t.Errorf("stories = %+v", data.Stories)
	}
	if len(data.Tasks) != 1 {
		t.Errorf("tasks = %d", len(data.Tasks))
	}
	// AC2: stages assembled from mirror ledger + seat (not empty for engaged backlog).
	if len(data.Stories[0].Stages) == 0 {
		t.Errorf("expected progress stages from mirror ledger/seat, got none: %+v", data.Stories[0])
	}
	if len(data.DocKinds) == 0 {
		t.Error("expected DocKinds groups")
	}
	// Workflows built from workflows kind docs.
	if len(data.Workflows) != 1 || data.Workflows[0].Provenance != "authored" {
		t.Errorf("workflows = %+v", data.Workflows)
	}
	if data.TopBar.MirrorRO != true || data.TopBar.IdentityEmail != "op@example.com" {
		t.Errorf("topbar RO/identity: %+v", data.TopBar)
	}
	// Existing fixture seat is in_flight+!stale but does not set story_seat — engagement
	// requires explicit story_seat (sty_01ba9482). Stages still use decodeLiveSeats.
	if data.EngagementCount != 0 {
		t.Errorf("EngagementCount = %d without story_seat, want 0", data.EngagementCount)
	}
}

// TestEngagedStorySeatIDsPredicate (sty_01ba9482): engagement counts non-stale
// story_seat only — not task seats, not stale rows, not missing story_seat.
func TestEngagedStorySeatIDsPredicate(t *testing.T) {
	got := engagedStorySeatIDs([]seatPayload{
		{ID: "sty_live", StorySeat: true, Stale: false},
		{ID: "sty_stale", StorySeat: true, Stale: true},
		{ID: "tsk_1", StorySeat: false, Stale: false, InFlight: true},
		{ID: "sty_nofield", StorySeat: false, Stale: false, InFlight: true},
		{ID: "", StorySeat: true, Stale: false}, // empty id skipped
	})
	if len(got) != 1 || got[0] != "sty_live" {
		t.Fatalf("engaged ids = %v, want [sty_live]", got)
	}
	// Settled engaged lease (story_seat, !in_flight, !stale) still counts.
	got2 := engagedStorySeatIDs([]seatPayload{
		{ID: "sty_settled", StorySeat: true, InFlight: false, Stale: false},
	})
	if len(got2) != 1 || got2[0] != "sty_settled" {
		t.Fatalf("settled engaged = %v, want [sty_settled]", got2)
	}
	if n := len(engagedStorySeatIDs(nil)); n != 0 {
		t.Fatalf("nil seats count = %d", n)
	}
}

// TestEngagementCountAndChrome (sty_01ba9482 / sty_e4632f45): zero chip hidden;
// non-zero identifies the story; stale/clear hide chip again; fragment soft-refresh.
func TestEngagementCountAndChrome(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-eng"
	if _, err := s.TouchPartition(ctx, rk, "eng", now); err != nil {
		t.Fatal(err)
	}
	story := workitem.Item{
		ID: "sty_e1", Kind: workitem.KindStory, Title: "Engaged Story",
		Status: workitem.StatusInProgress, Category: "feature",
		UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "eng", RepoRoot: "/e"})
	_ = s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{{ID: "sty_e1", Payload: string(sb)}}, now)
	_ = s.ReplaceKind(ctx, rk, "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, now)

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	// Zero: chip absent on project page and fragment (sty_e4632f45).
	zeroPage := httpGetBody(t, srv.URL+"/r/eng/")
	for _, forbid := range []string{
		`class="n-engaged"`, `data-engagement-count="0"`, `0 engaged`,
		`title="no story engaged"`,
	} {
		if strings.Contains(zeroPage, forbid) {
			t.Errorf("zero page must not contain %q", forbid)
		}
	}
	if !strings.Contains(zeroPage, `class="tab-cluster"`) || !strings.Contains(zeroPage, `class="tab-label"`) {
		t.Error("zero page missing tab-cluster or tab-label chrome")
	}
	zeroFrag := httpGetBody(t, srv.URL+"/r/eng/fragment/engagement")
	if strings.Contains(zeroFrag, "<!doctype") {
		t.Error("engagement fragment must not be a full document")
	}
	if strings.TrimSpace(zeroFrag) != "" {
		t.Errorf("zero fragment must be empty, got: %q", zeroFrag)
	}
	if strings.Contains(zeroFrag, "n-engaged") {
		t.Errorf("zero fragment must not contain n-engaged: %s", zeroFrag)
	}
	data0, _, err := mirrorLoadPanels(ctx, s, rk, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if data0.EngagementCount != 0 || len(data0.EngagedStoryIDs) != 0 {
		t.Errorf("empty seats: count=%d ids=%v", data0.EngagementCount, data0.EngagedStoryIDs)
	}

	// Seed a backlog story so both chips render together (sty_c7ab5180 AC3).
	backlogStory := workitem.Item{
		ID: "sty_bl", Kind: workitem.KindStory, Title: "Backlog Story",
		Status: workitem.StatusBacklog, Category: "feature",
		UpdatedAt: now, CreatedAt: now,
	}
	blb, _ := json.Marshal(backlogStory)
	if err := s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{
		{ID: "sty_e1", Payload: string(sb)},
		{ID: "sty_bl", Payload: string(blb)},
	}, now); err != nil {
		t.Fatal(err)
	}

	// Non-stale story_seat → count 1 + identity/link; chip present.
	liveSeat, _ := json.Marshal(map[string]any{
		"id": "sty_e1", "kind": "story", "story_seat": true,
		"in_flight": false, "stale": false,
	})
	if err := s.ReplaceKind(ctx, rk, "seat", []mirror.ItemRow{{ID: "sty_e1", Payload: string(liveSeat)}}, now); err != nil {
		t.Fatal(err)
	}
	data1, _, err := mirrorLoadPanels(ctx, s, rk, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if data1.EngagementCount != 1 || len(data1.EngagedStoryIDs) != 1 || data1.EngagedStoryIDs[0] != "sty_e1" {
		t.Fatalf("live seat: count=%d ids=%v", data1.EngagementCount, data1.EngagedStoryIDs)
	}
	// Stages predicate unchanged: settled !in_flight does not set seatHeld stages path —
	// still no regression: load still succeeds and stories present.
	if len(data1.Stories) != 2 {
		t.Fatalf("stories = %d", len(data1.Stories))
	}
	onePage := httpGetBody(t, srv.URL+"/r/eng/")
	for _, want := range []string{
		`data-engagement-count="1"`, `has-engaged`, `href="story/sty_e1"`,
		`1 engaged`, `title="engaged: sty_e1"`,
		// Both chips, number-first (sty_c7ab5180).
		`1 backlog`,
		// Badge is a sibling of the Stories tab <a> (tab-cluster), not nested
		// inside it — nested anchors misalign the chip in the browser.
		`class="tab-cluster"`,
	} {
		if !strings.Contains(onePage, want) {
			t.Errorf("engaged page missing %q", want)
		}
	}
	// Stories tab link must not contain the story link (invalid nested <a>).
	if i := strings.Index(onePage, `data-panel="stories"`); i >= 0 {
		// Slice from the Stories tab open tag through its closing </a>.
		rest := onePage[i:]
		if end := strings.Index(rest, "</a>"); end >= 0 {
			storiesTab := rest[:end]
			if strings.Contains(storiesTab, "n-engaged-link") || strings.Contains(storiesTab, `href="story/`) {
				t.Errorf("Stories tab <a> must not nest the engaged story link; got: %s", storiesTab)
			}
		}
	}
	oneFrag := httpGetBody(t, srv.URL+"/r/eng/fragment/engagement")
	if !strings.Contains(oneFrag, `data-engagement-count="1"`) || !strings.Contains(oneFrag, "sty_e1") {
		t.Errorf("engaged fragment: %s", oneFrag)
	}

	// Stale story_seat does not count → chip absent again.
	staleSeat, _ := json.Marshal(map[string]any{
		"id": "sty_e1", "story_seat": true, "in_flight": true, "stale": true,
	})
	_ = s.ReplaceKind(ctx, rk, "seat", []mirror.ItemRow{{ID: "sty_e1", Payload: string(staleSeat)}}, now)
	dataStale, _, _ := mirrorLoadPanels(ctx, s, rk, "eng")
	if dataStale.EngagementCount != 0 {
		t.Errorf("stale seat counted: %d", dataStale.EngagementCount)
	}
	stalePage := httpGetBody(t, srv.URL+"/r/eng/")
	if strings.Contains(stalePage, `class="n-engaged"`) || strings.Contains(stalePage, "0 engaged") {
		t.Error("stale seat must hide engagement chip")
	}
	staleFrag := httpGetBody(t, srv.URL+"/r/eng/fragment/engagement")
	if strings.TrimSpace(staleFrag) != "" {
		t.Errorf("stale fragment must be empty, got: %q", staleFrag)
	}

	// Clear seats → 0; chip still absent.
	_ = s.ReplaceKind(ctx, rk, "seat", nil, now)
	dataClear, _, _ := mirrorLoadPanels(ctx, s, rk, "eng")
	if dataClear.EngagementCount != 0 {
		t.Errorf("cleared seats: %d", dataClear.EngagementCount)
	}
	clearPage := httpGetBody(t, srv.URL+"/r/eng/")
	if strings.Contains(clearPage, `class="n-engaged"`) || strings.Contains(clearPage, `data-engagement-count`) {
		t.Error("cleared page must not render engagement chip")
	}

	// app.js soft-refresh contract: function name + fragment path + insert/remove.
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "refreshEngagementBadge") || !strings.Contains(src, "fragment/engagement") {
		t.Error("app.js missing engagement soft-refresh helpers")
	}
	if !strings.Contains(src, "appendChild") || !strings.Contains(src, "tab-cluster") {
		t.Error("app.js refreshEngagementBadge must insert into .tab-cluster on 0→n")
	}
	if !strings.Contains(src, ".remove()") && !strings.Contains(src, "cur.remove()") {
		t.Error("app.js refreshEngagementBadge must remove chip on n→0")
	}
}

// TestMirrorProjectPageRendersTemplates seeds a partition and checks HTML chrome.
func TestMirrorProjectPageRendersTemplates(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-html"
	if _, err := s.TouchPartition(ctx, rk, "proj", now); err != nil {
		t.Fatal(err)
	}
	story := workitem.Item{
		ID: "sty_h", Kind: workitem.KindStory, Title: "HTML Story",
		Status: workitem.StatusBacklog, Category: "chore",
		UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p", FooterEmail: "a@b.c"})
	_ = s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{{ID: "sty_h", Payload: string(sb)}}, now)
	_ = s.ReplaceKind(ctx, rk, "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, now)

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/r/proj/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, want := range []string{
		"HTML Story", "Stories", "Tasks", "Workflow", "Documents",
		`class="topbar"`, `class="theme-toggle"`, "a@b.c",
		`<base href="/r/proj/">`,
		// sty_eea989dd: identity strip explains RO local UI (no bare "mirror" mode pill).
		`title="Read-only local UI — project data pushed by the CLI (not live-edited here)"`,
		`aria-label="Operator identity a@b.c; read-only local UI, project data pushed by the CLI"`,
		">Install</a>", ">Docs</a>", ">Projects</a>", ">Logs</a>",
		"https://satelle.dev/install", "https://satelle.dev/docs",
		// sty_e4632f45: engagement chip hidden at 0; tab labels use bold-ghost spans.
		`class="tab-label"`, `class="tab-cluster"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
	for _, forbid := range []string{
		`class="n-engaged"`, `data-engagement-count="0"`, `0 engaged`,
	} {
		if strings.Contains(body, forbid) {
			t.Errorf("project page must not show zero engagement chip %q", forbid)
		}
	}
	if strings.Contains(body, ">mirror</span>") {
		t.Error("project page must not render bare mirror mode pill")
	}

	// sty_0612af6b: the satelle user is named once, in the top bar; the shared
	// footer carries the product/version only — no email, no mailto link.
	fStart := strings.Index(body, `<footer class="site-footer">`)
	fEnd := strings.Index(body[max(fStart, 0):], `</footer>`)
	if fStart < 0 || fEnd < 0 {
		t.Fatalf("project page missing site footer:\n%s", body)
	}
	footer := body[fStart : fStart+fEnd]
	if !strings.Contains(footer, `class="footer-version"`) {
		t.Errorf("footer missing version span: %s", footer)
	}
	for _, forbid := range []string{"a@b.c", "mailto:"} {
		if strings.Contains(footer, forbid) {
			t.Errorf("footer must not contain %q: %s", forbid, footer)
		}
	}
	if outside := body[:fStart] + body[fStart+fEnd:]; !strings.Contains(outside, "a@b.c") {
		t.Error("identity email must still appear outside the footer (top bar)")
	}

	// Workspace landing.
	resp2, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	land := string(raw2)
	if !strings.Contains(land, "workspace") || !strings.Contains(land, `/r/proj/`) {
		t.Errorf("landing missing partition link:\n%s", land)
	}
	// Empty identity on landing: no mode pill (sty_eea989dd).
	if strings.Contains(land, ">mirror</span>") {
		t.Error("landing must not show bare mirror pill when identity is empty")
	}
	for _, want := range []string{">Install</a>", ">Docs</a>", ">Projects</a>", ">Logs</a>"} {
		if !strings.Contains(land, want) {
			t.Errorf("landing missing nav %q", want)
		}
	}
	// Non-ingest POST rejected.
	preq, _ := http.NewRequest(http.MethodPost, srv.URL+"/theme", strings.NewReader("theme=dark"))
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode == 200 || presp.StatusCode == 204 {
		t.Errorf("POST /theme must not succeed, got %d", presp.StatusCode)
	}
}

// TestStoryDetailPresentsRouteDocument (sty_085e1a5a AC2): a story in flight
// shows its route with each step's outcome, taken from the route DOCUMENT the
// engine wrote — the web layer presents that artifact and lifts it out of the
// generic attachment list, so nothing is re-derived here.
func TestStoryDetailPresentsRouteDocument(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-route"
	if _, err := s.TouchPartition(ctx, rk, "demo", now); err != nil {
		t.Fatal(err)
	}

	story := workitem.Item{
		ID: "sty_r", Kind: workitem.KindStory, Title: "Routed",
		Status: workitem.StatusInProgress, Category: "feature", UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	routeDoc, _ := json.Marshal(map[string]string{
		"name": routeDocName, "type": "route",
		"body": "## Route\n\n* 3. **in_progress**\n\n## Outcomes\n\n### plan → in_progress — accepted\n\n- **satelle-story-plan-review** — ACCEPT\n",
	})
	planDoc, _ := json.Marshal(map[string]string{"name": "plan", "type": "plan", "body": "# Plan\n"})
	if err := s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{{ID: "sty_r", Payload: string(sb)}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKind(ctx, rk, "story_doc", []mirror.ItemRow{
		{ID: "sty_r/route", Payload: string(routeDoc)},
		{ID: "sty_r/plan", Payload: string(planDoc)},
	}, now); err != nil {
		t.Fatal(err)
	}

	d, _, err := mirrorLoadDetail(ctx, s, rk, "story", "sty_r")
	if err != nil {
		t.Fatal(err)
	}
	if d.Route == nil {
		t.Fatal("the route document should be lifted into detailData.Route")
	}
	for _, name := range []string{routeDocName} {
		for _, doc := range d.Docs {
			if doc.Name == name {
				t.Errorf("%s must not also appear in Documents (shown twice)", name)
			}
		}
	}
	if len(d.Docs) != 1 || d.Docs[0].Name != "plan" {
		t.Errorf("Docs = %+v, want just the plan document", d.Docs)
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "itemDetail", d); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"<h4>Route</h4>", "in_progress", "satelle-story-plan-review", "ACCEPT"} {
		if !strings.Contains(html, want) {
			t.Errorf("story detail missing %q:\n%s", want, html)
		}
	}

	// A story that has not transitioned yet gets no fabricated route.
	if err := s.ReplaceKind(ctx, rk, "story_doc", nil, now); err != nil {
		t.Fatal(err)
	}
	d2, _, err := mirrorLoadDetail(ctx, s, rk, "story", "sty_r")
	if err != nil {
		t.Fatal(err)
	}
	if d2.Route != nil {
		t.Error("no route document ⇒ no Route section; the web layer must not derive one")
	}
}

// TestMirrorTimelineDocLink (sty_49666ca9): story_doc_attached ledger rows
// render as named timeline items; resolvable docs get a real link into the
// standalone page hash-anchor; legacy payload-less rows still name the doc;
// unresolvable/binary rows stay named but unlinked; the ← back-link restores
// the expanded project row.
func TestMirrorTimelineDocLink(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-tl-doc"
	slug := "tldoc"
	if _, err := s.TouchPartition(ctx, rk, slug, now); err != nil {
		t.Fatal(err)
	}

	id := "sty_tl"
	story := workitem.Item{
		ID: id, Kind: workitem.KindStory, Title: "Timeline Doc Link",
		Status: workitem.StatusInProgress, Category: "feature",
		UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	planDoc, _ := json.Marshal(map[string]string{
		"name": "plan", "type": "plan", "body": "# Plan\n\nDo the thing.\n",
	})
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: slug, RepoRoot: "/tmp/" + slug})

	// Payload-carrying attach (new path).
	ledPayload, _ := json.Marshal(map[string]any{
		"id": "evt_pay", "story_id": id, "kind": "story_doc_attached",
		"body": `attached plan document "plan"`, "created_at": now,
		"payload": map[string]string{"name": "plan", "type": "plan"},
	})
	// Legacy payload-less attach (body parse fallback).
	ledLegacy, _ := json.Marshal(map[string]any{
		"id": "evt_leg", "story_id": id, "kind": "story_doc_attached",
		"body": `attached plan document "plan"`, "created_at": now.Add(-time.Minute),
		"payload": map[string]any{},
	})
	// Binary / unresolvable — named but no href (doc not in story_doc set).
	ledBin, _ := json.Marshal(map[string]any{
		"id": "evt_bin", "story_id": id, "kind": "story_doc_attached",
		"body":       `attached image binary "shot.png" (image/png, 12 bytes, sha256:abc)`,
		"created_at": now.Add(-2 * time.Minute),
		"payload":    map[string]any{"name": "shot.png", "type": "image", "binary": true},
	})

	for _, r := range []struct {
		kind string
		rows []mirror.ItemRow
	}{
		{"story", []mirror.ItemRow{{ID: id, Payload: string(sb)}}},
		{"story_doc", []mirror.ItemRow{{ID: id + "/plan", Payload: string(planDoc)}}},
		{"ledger_event", []mirror.ItemRow{
			{ID: "evt_pay", Payload: string(ledPayload)},
			{ID: "evt_leg", Payload: string(ledLegacy)},
			{ID: "evt_bin", Payload: string(ledBin)},
		}},
		{"identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}},
	} {
		if err := s.ReplaceKind(ctx, rk, r.kind, r.rows, now); err != nil {
			t.Fatal(err)
		}
	}

	d, _, err := mirrorLoadDetail(ctx, s, rk, "story", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Docs) != 1 || d.Docs[0].Name != "plan" || d.Docs[0].Anchor != "doc-plan" {
		t.Fatalf("Docs = %+v, want plan with Anchor doc-plan", d.Docs)
	}

	var linked, legacyNamed, binUnlinked int
	for _, ev := range d.Events {
		if ev.Kind != "story_doc_attached" {
			continue
		}
		if ev.DocName == "" {
			t.Errorf("story_doc_attached %s missing DocName", ev.ID)
		}
		switch ev.ID {
		case "evt_pay":
			if ev.DocName != "plan" || ev.DocType != "plan" {
				t.Errorf("payload event: DocName/Type = %q/%q", ev.DocName, ev.DocType)
			}
			wantHref := "story/" + id + "#doc-plan"
			if ev.DocHref != wantHref {
				t.Errorf("payload DocHref = %q, want %q", ev.DocHref, wantHref)
			}
			linked++
		case "evt_leg":
			if ev.DocName != "plan" || ev.DocType != "plan" {
				t.Errorf("legacy event: DocName/Type = %q/%q", ev.DocName, ev.DocType)
			}
			if ev.DocHref != "story/"+id+"#doc-plan" {
				t.Errorf("legacy DocHref = %q (body-parse should still link when doc present)", ev.DocHref)
			}
			legacyNamed++
		case "evt_bin":
			if ev.DocName != "shot.png" || ev.DocType != "image" {
				t.Errorf("binary event: DocName/Type = %q/%q", ev.DocName, ev.DocType)
			}
			if ev.DocHref != "" {
				t.Errorf("binary DocHref = %q, want empty (unresolvable)", ev.DocHref)
			}
			binUnlinked++
		}
	}
	if linked != 1 || legacyNamed != 1 || binUnlinked != 1 {
		t.Fatalf("counts linked=%d legacy=%d bin=%d", linked, legacyNamed, binUnlinked)
	}

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	body := httpGetBody(t, srv.URL+"/r/"+slug+"/story/"+id)
	for _, want := range []string{
		`class="ev-doc"`,
		`href="story/` + id + `#doc-plan"`,
		`id="doc-plan"`,
		`class="back-link"`,
		`?expand=` + id + `#stories`,
		`>plan <span class="doc-item-type">plan</span>`,
		`shot.png`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	// Unresolvable binary must be named but not linked.
	if strings.Contains(body, `href="story/`+id+`#doc-shot-png"`) ||
		strings.Contains(body, `href="story/`+id+`#doc-shot.png"`) {
		t.Error("binary attachment must not produce a dead timeline href")
	}
	// Named plain-text for the binary row (no wrapping <a>).
	if !strings.Contains(body, `<div class="ev-doc">shot.png`) {
		t.Error("binary row should render named-but-unlinked ev-doc")
	}
}

const costWFDone = `[meta]
name = "done"
type = "workflow"
scope = "project"
description = "fixture"

["*"]
obligations = ["raised", "planned", "parked", "closed"]
`

const costWFStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
description = "fixture"

[raised]
status = "backlog"
start = true

[planned]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[parked]
status = "blocked"
agent = "reviewer"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["planned"]
`
