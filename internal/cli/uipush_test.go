package cli

import (
	"context"
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

// Each command's ledger writes reach this home's satelled; workspace add
// seeds it and workspace remove drops the partition through its ingest.
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

	if out := must(t, "workspace", "add"); !strings.Contains(out, "seeded satelled at "+srv.URL) {
		t.Fatalf("workspace add: %s", out)
	}
	if wf := get("/r/" + slug() + "/fragment/workflow/default"); !strings.Contains(wf, `class="route-line"`) {
		t.Errorf("the seed did not push the workflow:\n%s", wf)
	}

	must(t, "story", "set", id, "--status", "done")
	flushUI(context.Background())
	if page := get("/r/" + slug() + "/story/" + id); !strings.Contains(page, "outcome-moved") || !strings.Contains(page, "st-terminal") {
		t.Errorf("the transition did not reach satelled:\n%s", page)
	}

	must(t, "workspace", "remove")
	if parts, _ := ms.ListPartitions(context.Background()); len(parts) != 0 {
		t.Errorf("workspace remove left %d partition(s)", len(parts))
	}
}
