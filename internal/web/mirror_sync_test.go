package web

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// devStore is the store a satelle.dev-bound pass records.
const devStore = "satelle.dev project shared at https://satelle.dev"

// sto_5c706973 AC5: a failure after a success names the reason and when the
// project last synced.
func TestProjectPageNamesFailureAndLastSync(t *testing.T) {
	okAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	body := projectPageWithSync(t, func(repo string) {
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore}, okAt); err != nil {
			t.Fatal(err)
		}
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore, Reason: "rpc error: code = Unavailable desc = down"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(body, `remote: <span class="sync-fail">sync failing</span>`) {
		t.Fatalf("project page must read sync failing:\n%s", body)
	}
	fail := sectionOf(t, body, `<div class="sync-fail-body"`)
	if !strings.Contains(fail, ">rpc error: code = Unavailable desc = down<") {
		t.Errorf("the reason must be a visible text node:\n%s", fail)
	}
	if !strings.Contains(fail, "last synced <time") || !strings.Contains(fail, `datetime="`+okAt.Format(time.RFC3339)+`"`) {
		t.Errorf("the failure must name the last success:\n%s", fail)
	}
}

// sto_5c706973 AC5: a success names when, and with which store.
func TestProjectPageShowsLastSyncWithStore(t *testing.T) {
	okAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	body := projectPageWithSync(t, func(repo string) {
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore}, okAt); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(body, `title="Last sync with `+devStore+` at `) {
		t.Errorf("the store must be named in the title:\n%s", body)
	}
	if !strings.Contains(body, `>synced <time class="rel-time" datetime="`+okAt.Format(time.RFC3339)+`"`) {
		t.Errorf("the success instant must be a <time>:\n%s", body)
	}
	if strings.Contains(body, "sync failing") {
		t.Errorf("a success must not read failing:\n%s", body)
	}
}

// sto_5c706973 AC5: a repo whose store is local reads local.
func TestProjectPageShowsLocalStore(t *testing.T) {
	body := projectPageWithSync(t, func(repo string) {
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: syncstate.LocalStore}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(body, `remote: <span class="sync-local"`) || !strings.Contains(body, ">local</span>") {
		t.Fatalf("project page must read local:\n%s", body)
	}
}

func TestLandingRendersStandingSyncFailure(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore, Reason: "hosted 505"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	body := rr.Body.String()
	cell := updatedCell(t, body)
	if !strings.Contains(cell, "Sync Failing") {
		t.Fatalf("Sync Failing tag missing from Updated cell:\n%s", cell)
	}
	if !strings.Contains(cell, `class="sync-tag-fail"`) {
		t.Fatalf("fail tag class missing from Updated cell:\n%s", cell)
	}
	for _, gone := range []string{"local:", "remote:", "synced"} {
		if strings.Contains(cell, gone) {
			t.Fatalf("Updated cell must not contain %q:\n%s", gone, cell)
		}
	}
	if strings.Contains(body, "sync-fail-detail") {
		t.Fatal("landing must not render a sync-fail-detail row")
	}
	if strings.Contains(body, "hosted 505") {
		t.Fatalf("fail reason must not appear on the landing:\n%s", body)
	}
	if strings.Contains(body, "logged to") || strings.Contains(body, "server.log") {
		t.Fatalf("log path must not appear on the landing:\n%s", body)
	}
	if n := strings.Count(body, `tr class="row" data-slug=`); n != 1 {
		t.Fatalf("landing must have exactly one primary row per partition, got %d:\n%s", n, body)
	}
}

func updatedCell(t *testing.T, body string) string {
	t.Helper()
	const open = `<td class="updated-cell">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatalf("updated-cell missing:\n%s", body)
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "</td>")
	if j < 0 {
		t.Fatalf("updated-cell unclosed:\n%s", rest)
	}
	return rest[:j]
}

func TestProjectPageRendersStandingSyncFailure(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore, Reason: "hosted 505"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/r/demo/", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `remote: <span class="sync-fail">sync failing`) {
		t.Fatalf("project page sync failing must follow remote::\n%s", body)
	}
	if strings.Contains(body, `title="hosted 505"`) {
		t.Fatal("reason must not live only in a title tooltip")
	}
	if !strings.Contains(body, ">hosted 505<") {
		t.Fatalf("project page reason not a visible text node:\n%s", body)
	}
	if strings.Contains(body, "logged to") || strings.Contains(body, "server.log") {
		t.Fatalf("no sync writes satelled's log; the page must not point at it:\n%s", body)
	}
}

func TestLandingUpdatedCellIsLocalOnly(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	body := rr.Body.String()
	cell := updatedCell(t, body)
	if !strings.Contains(cell, `class="rel-time"`) {
		t.Fatalf("Updated cell must carry the local ingest stamp:\n%s", cell)
	}
	for _, gone := range []string{"local:", "remote:", "synced", "sync-ok", "Sync Failing"} {
		if strings.Contains(cell, gone) {
			t.Fatalf("Updated cell must not contain %q:\n%s", gone, cell)
		}
	}
}

func TestProjectPageNamesBothTimes(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/r/demo/", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "local: updated <time") {
		t.Fatalf("project header missing local: updated outside time.rel-time:\n%s", body)
	}
	if !strings.Contains(body, "remote:") {
		t.Fatalf("project header missing remote: prefix:\n%s", body)
	}
	if !strings.Contains(body, ">synced <time") {
		t.Fatalf("project header must show pushed outside time.rel-time:\n%s", body)
	}
}

func TestLandingRendersLocalOnly(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: syncstate.LocalStore}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	body := rr.Body.String()
	cell := updatedCell(t, body)
	if !strings.Contains(cell, `class="rel-time"`) {
		t.Fatalf("Updated cell must carry the local ingest stamp:\n%s", cell)
	}
	if strings.Contains(cell, "sync-local") || strings.Contains(cell, "remote:") {
		t.Fatalf("landing Updated cell must not show the local-only remote marker:\n%s", cell)
	}
}

func TestLandingOmitsSyncMarkerWhenNoState(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	body := rr.Body.String()
	if strings.Contains(body, "sync-fail") {
		t.Fatalf("no-state partition must not show a failure marker:\n%s", body)
	}
}

// renderLocalAfter records a pass via record, then the local pass a repo whose
// store is local writes, and renders path (sty_3df5f95a).
func renderLocalAfter(t *testing.T, path string, record func(home, repo string) error) string {
	t.Helper()
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := record(config.GlobalDir(), repo); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: syncstate.LocalStore}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	NewMirror(s).Handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	return rr.Body.String()
}

func recordFailure(home, repo string) error {
	return syncstate.RecordPass(home, repo, syncstate.Pass{Store: devStore, Reason: "hosted 505"}, time.Now())
}

func TestProjectPageLocalHidesStaleFailure(t *testing.T) {
	body := renderLocalAfter(t, "/r/demo/", recordFailure)
	if !strings.Contains(body, `class="sync-local"`) {
		t.Fatalf("project header must read local:\n%s", body)
	}
	for _, gone := range []string{"sync failing", "sync-fail", ">hosted 505<", "logged to"} {
		if strings.Contains(body, gone) {
			t.Fatalf("local project page must not contain %q:\n%s", gone, body)
		}
	}
}

func TestProjectPageLocalHidesStaleSuccess(t *testing.T) {
	body := renderLocalAfter(t, "/r/demo/", func(home, repo string) error {
		return syncstate.RecordPass(home, repo, syncstate.Pass{Store: devStore}, time.Now().Add(-time.Hour))
	})
	if !strings.Contains(body, `class="sync-local"`) {
		t.Fatalf("project header must read local:\n%s", body)
	}
	for _, gone := range []string{">synced <time", "sync-ok"} {
		if strings.Contains(body, gone) {
			t.Fatalf("local project page must not contain %q:\n%s", gone, body)
		}
	}
}

func TestLandingLocalHidesStaleFailure(t *testing.T) {
	cell := updatedCell(t, renderLocalAfter(t, "/", recordFailure))
	for _, gone := range []string{"Sync Failing", "sync-tag-fail"} {
		if strings.Contains(cell, gone) {
			t.Fatalf("local landing Updated cell must not contain %q:\n%s", gone, cell)
		}
	}
}

// projectPageWithSync renders the project page of a partition whose repo has
// the recorded sync state record writes.
func projectPageWithSync(t *testing.T, record func(repo string)) string {
	t.Helper()
	return pageWithSync(t, "/r/demo/", record)
}

// pageWithSync renders path ("/" the landing, "/r/demo/" the project page)
// over one partition whose repo has the recorded sync state record writes.
func pageWithSync(t *testing.T, path string, record func(repo string)) string {
	t.Helper()
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"` + repo + `"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	record(repo)
	rr := httptest.NewRecorder()
	NewMirror(s).Handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	return rr.Body.String()
}

// recordUpgradeAfterFailure records a failing pass and then the upgrade
// outcome autosync records for a pre-1.0 repo (sto_0f76dd4e).
func recordUpgradeAfterFailure(t *testing.T) func(repo string) {
	return func(repo string) {
		if err := recordFailure(config.GlobalDir(), repo); err != nil {
			t.Fatal(err)
		}
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Upgrade: "satelle upgrade --archive"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// AC2 (sto_0f76dd4e): the landing shows a pre-1.0 repo as Upgrade needed with
// the command, not Sync Failing.
func TestLandingShowsUpgradeNeeded(t *testing.T) {
	cell := updatedCell(t, pageWithSync(t, "/", recordUpgradeAfterFailure(t)))
	for _, want := range []string{"Upgrade needed", `class="sync-tag-upgrade"`, "<code>satelle upgrade --archive</code>"} {
		if !strings.Contains(cell, want) {
			t.Errorf("Updated cell lacks %q:\n%s", want, cell)
		}
	}
	for _, gone := range []string{"Sync Failing", "sync-tag-fail"} {
		if strings.Contains(cell, gone) {
			t.Errorf("Updated cell must not contain %q:\n%s", gone, cell)
		}
	}
}

// AC2 (sto_0f76dd4e): the project page reads upgrade needed and names the
// command to run in the repo; nothing reads sync failing.
func TestProjectPageShowsUpgradeNeeded(t *testing.T) {
	body := projectPageWithSync(t, recordUpgradeAfterFailure(t))
	if !strings.Contains(body, `remote: <span class="sync-upgrade"`) {
		t.Fatalf("project header must read upgrade needed:\n%s", body)
	}
	if up := sectionOf(t, body, `<div class="sync-upgrade-body"`); !strings.Contains(up, "<code>satelle upgrade --archive</code>") {
		t.Errorf("the upgrade body must name the command:\n%s", up)
	}
	for _, gone := range []string{"sync failing", "sync-fail-body"} {
		if strings.Contains(body, gone) {
			t.Errorf("project page must not contain %q:\n%s", gone, body)
		}
	}
}

// sty_c203fd62 AC1: a push that only held stories shows them as held — neutral
// class, ids and reasons — and never as sync failing.
func TestProjectPageShowsHeldStoriesAsHeldNotFailing(t *testing.T) {
	body := projectPageWithSync(t, func(repo string) {
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := syncstate.RecordPushHeld(config.GlobalDir(), repo, map[string]string{
			"sty_216d5205": "uncommitted changes in /wt/sty_216d5205",
		}); err != nil {
			t.Fatal(err)
		}
	})
	held := sectionOf(t, body, `<div class="sync-held"`)
	if !strings.Contains(held, "sty_216d5205") || !strings.Contains(held, "uncommitted changes in /wt/sty_216d5205") {
		t.Fatalf("held section must name the id and its reason:\n%s", held)
	}
	for _, gone := range []string{"sync failing", "sync-fail", "Sync Failing"} {
		if strings.Contains(body, gone) {
			t.Errorf("a held-only push must not render %q:\n%s", gone, body)
		}
	}
	if !strings.Contains(body, `remote: <span class="sync-ok"`) {
		t.Errorf("a held-only pass is a synced remote:\n%s", body)
	}
}

// sty_c203fd62 AC2: a real refusal still shows sync failing with only the
// refusal as its reason; the held story is shown apart, as held.
func TestProjectPageShowsRefusalAsFailingAndHeldApart(t *testing.T) {
	body := projectPageWithSync(t, func(repo string) {
		if err := syncstate.RecordPass(config.GlobalDir(), repo, syncstate.Pass{Store: devStore, Reason: "the story sty_r was not published: archived task refused"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := syncstate.RecordPushHeld(config.GlobalDir(), repo, map[string]string{
			"sty_216d5205": "uncommitted changes in /wt/sty_216d5205",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(body, `remote: <span class="sync-fail">sync failing`) {
		t.Fatalf("a refusal must show sync failing:\n%s", body)
	}
	fail := sectionOf(t, body, `<div class="sync-fail-body"`)
	if !strings.Contains(fail, `<span class="sync-fail-reason">the story sty_r was not published: archived task refused</span>`) {
		t.Errorf("the refusal must be the failure reason:\n%s", fail)
	}
	if strings.Contains(fail, "sty_216d5205") {
		t.Errorf("a held story must not appear inside the failure:\n%s", fail)
	}
	held := sectionOf(t, body, `<div class="sync-held"`)
	if !strings.Contains(held, "sty_216d5205") || strings.Contains(held, "sty_r ") {
		t.Errorf("held section must name only the held story:\n%s", held)
	}
}

// sectionOf returns the header div opened by open, up to the next top-level
// header element; it fails the test when open is missing.
func sectionOf(t *testing.T, body, open string) string {
	t.Helper()
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatalf("%s missing:\n%s", open, body)
	}
	rest := body[i:]
	if j := strings.Index(rest, "\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
