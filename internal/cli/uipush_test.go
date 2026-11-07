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
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/web"
)

// Each command's ledger writes reach this home's satelled; project add
// seeds it and project remove drops the partition through its ingest.
func TestCommandsPushToSatelled(t *testing.T) {
	isolate(t)
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

// A seed satelled refuses shows in `satelle status` with its cause until a
// full snapshot lands; a satelled that is not listening is not a failed seed.
func TestStatusNamesAFailedSeed(t *testing.T) {
	isolate(t)
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
