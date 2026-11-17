package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/web"
)

func TestCommandsPushToSatelled(t *testing.T) {
	isolate(t)
	outsideTemp(t)
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	srv := httptest.NewServer(web.NewMirrorWithInstance(ms, config.CurrentInstanceID()).Handler)
	t.Cleanup(srv.Close)
	t.Setenv("SATELLE_SERVER_ENDPOINT", srv.URL)
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	slug := func() string {
		parts, _ := ms.ListPartitions(context.Background())
		if len(parts) != 1 {
			t.Fatalf("partitions = %d, want 1", len(parts))
		}
		return parts[0].Slug
	}

	must(t, "init")
	id := createID(t, "story", "create", "--title", "pushed by the cli", "--criteria", "lands")
	flushUI(context.Background())
	if list := get("/r/" + slug() + "/fragment/stories"); !strings.Contains(list, "pushed by the cli") {
		t.Fatalf("the create did not reach satelled:\n%s", list)
	}

	if out := must(t, "project", "add"); !strings.Contains(out, "seeded satelled at "+srv.URL) {
		t.Fatalf("project add: %s", out)
	}
	if wf := get("/r/" + slug() + "/fragment/workflow/default"); !strings.Contains(wf, `class="route-line"`) {
		t.Errorf("the seed did not push the workflow:\n%s", wf)
	}

	must(t, "story", "set", id, "--status", "done")
	flushUI(context.Background())
	if page := get("/r/" + slug() + "/story/" + id); !strings.Contains(page, "outcome-moved") || !strings.Contains(page, "st-terminal") {
		t.Errorf("the transition did not reach satelled:\n%s", page)
	}

	must(t, "project", "remove")
	if parts, _ := ms.ListPartitions(context.Background()); len(parts) != 0 {
		t.Errorf("project remove left %d partition(s)", len(parts))
	}
}

func TestStatusNamesAFailedSeed(t *testing.T) {
	isolate(t)
	outsideTemp(t)
	const cause = `mirror: snapshot has duplicate item kind=ledger_event id="deadbeef"`
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("X-Satelle-Instance", config.CurrentInstanceID())
			return
		}
		http.Error(w, cause, http.StatusInternalServerError)
	}))
	t.Cleanup(refusing.Close)

	must(t, "init")
	t.Setenv("SATELLE_SERVER_ENDPOINT", "http://127.0.0.1:1")
	must(t, "project", "add")
	if out := must(t, "status"); strings.Contains(out, "seed failed") {
		t.Fatalf("an unreachable satelled was recorded as a failed seed:\n%s", out)
	}

	t.Setenv("SATELLE_SERVER_ENDPOINT", refusing.URL)
	if out := must(t, "project", "add"); !strings.Contains(out, "not seeded") {
		t.Fatalf("project add: %s", out)
	}
	out := must(t, "status")
	if !strings.Contains(out, "seed failed") || !strings.Contains(out, `kind=ledger_event id="deadbeef"`) {
		t.Fatalf("status does not name the failed seed:\n%s", out)
	}
	var rep statusReport
	if err := json.Unmarshal([]byte(must(t, "status", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Seed == nil || !strings.Contains(rep.Seed.Reason, cause) || rep.Seed.Attempts != 1 || rep.Seed.FailedAt.IsZero() {
		t.Fatalf("status --json seed_failure = %+v", rep.Seed)
	}

	ms, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	srv := httptest.NewServer(web.NewMirrorWithInstance(ms, config.CurrentInstanceID()).Handler)
	t.Cleanup(srv.Close)
	t.Setenv("SATELLE_SERVER_ENDPOINT", srv.URL)
	if out := must(t, "project", "add"); !strings.Contains(out, "seeded satelled at "+srv.URL) {
		t.Fatalf("project add: %s", out)
	}
	if out := must(t, "status"); strings.Contains(out, "seed failed") {
		t.Fatalf("a landed seed did not clear the failure:\n%s", out)
	}
}

func mirrorFor(t *testing.T) *mirror.Store {
	t.Helper()
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	srv := httptest.NewServer(web.NewMirrorWithInstance(ms, config.CurrentInstanceID()).Handler)
	t.Cleanup(srv.Close)
	t.Setenv("SATELLE_SERVER_ENDPOINT", srv.URL)
	return ms
}

func partitionCount(t *testing.T, ms *mirror.Store) int {
	t.Helper()
	parts, err := ms.ListPartitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(parts)
}

func TestTempRepoPushesNothing(t *testing.T) {
	dir := isolate(t)
	ms := mirrorFor(t)
	r, err := findRepo(dir)
	if err == nil && pushesToUI(r) {
		t.Fatalf("a repo under the temp dir (%s) is said to push", dir)
	}
	must(t, "init")
	createID(t, "story", "create", "--title", "never pushed", "--criteria", "stays local")
	flushUI(context.Background())
	if n := partitionCount(t, ms); n != 0 {
		t.Fatalf("a command in a temp-dir repo left %d partition(s) in satelled", n)
	}
	out := must(t, "project", "add")
	if !strings.Contains(out, "added project") || !strings.Contains(out, "not seeded: ") || !strings.Contains(out, "is under the temp dir") {
		t.Fatalf("project add in a temp-dir repo: %s", out)
	}
	createID(t, "story", "create", "--title", "added, still not pushed", "--criteria", "stays local")
	flushUI(context.Background())
	r, err = findRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	confirmUI(context.Background(), r)
	if n := partitionCount(t, ms); n != 0 {
		t.Fatalf("an added temp-dir repo left %d partition(s) in satelled", n)
	}
}

func TestTempWorktreePushesNothing(t *testing.T) {
	main := isolate(t)
	ms := mirrorFor(t)
	rootT, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tempRootsOverride = []string{rootT}
	t.Cleanup(func() { tempRootsOverride = nil })
	must(t, "init")
	gittest.Git(t, main, "add", ".satelle")
	gittest.Git(t, main, "commit", "-q", "-m", "satelle")
	wt := filepath.Join(rootT, "wt")
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "side", wt)

	mr, err := findRepo(main)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := findRepo(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !pushesToUI(mr) || pushesToUI(wr) {
		t.Fatalf("pushesToUI: main %v (want true), temp worktree %v (want false)", pushesToUI(mr), pushesToUI(wr))
	}
	t.Chdir(wt)
	createID(t, "story", "create", "--title", "from the temp worktree", "--criteria", "stays local")
	flushUI(context.Background())
	if n := partitionCount(t, ms); n != 0 {
		t.Fatalf("a command in a temp-dir worktree left %d partition(s) in satelled", n)
	}
}
