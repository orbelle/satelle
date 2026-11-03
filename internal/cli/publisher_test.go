package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/web"
)

// TestUIDrainDeliversBeforeExit proves AC1/AC2: after runRoot returns (no
// sleeps), change ≥1 and snapshot == 1 for create and set; coalesced.
func TestUIDrainDeliversBeforeExit(t *testing.T) {
	var changeHits, snapHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ingest/change":
			changeHits.Add(1)
		case "/ingest/snapshot":
			snapHits.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	_ = tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	out, err := runRoot(t, "story", "create",
		"--title", "Drain Create",
		"--body", "both paths before exit",
		"--acceptance", "1. posted",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create: %v out=%s", err, out)
	}
	if changeHits.Load() < 1 || snapHits.Load() != 1 {
		t.Fatalf("after create: change=%d snap=%d", changeHits.Load(), snapHits.Load())
	}

	changeHits.Store(0)
	snapHits.Store(0)
	// story set can notify more than once (status/tags); snapshot must still be 1.
	out, err = runRoot(t, "story", "set", created.ID, "--add-tags", "drain:probe")
	if err != nil {
		t.Fatalf("set tags: %v\n%s", err, out)
	}
	if changeHits.Load() < 1 {
		t.Fatalf("after set: expected ≥1 change, got %d", changeHits.Load())
	}
	if snapHits.Load() != 1 {
		t.Fatalf("after set: expected exactly 1 snapshot (coalesced), got %d", snapHits.Load())
	}
}

// TestUIDrainConvergesMirror proves AC4: after story create against a real
// mirror ingest handler, the new story is listable — no manual ui push.
func TestUIDrainConvergesMirror(t *testing.T) {
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })

	webSrv := web.NewMirror(ms)
	srv := httptest.NewServer(webSrv.Handler)
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	out, err := runRoot(t, "story", "create",
		"--title", "Mirror Converge",
		"--body", "visible without ui push",
		"--acceptance", "1. in mirror",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse: %v out=%s", err, out)
	}

	repoKey := config.RepoKey(repo)
	items, err := ms.ListItems(context.Background(), repoKey, "story")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("mirror missing story %s after create (no ui push); items=%d", created.ID, len(items))
	}
}

// TestInitScaffoldsServerBlock proves init's committed satelle.toml points
// listen/push at machine [service] endpoint, not a repo [server] table.
func TestInitScaffoldsServerBlock(t *testing.T) {
	if strings.Contains(scaffoldToml, "\n[server]\n") || strings.Contains(scaffoldToml, "\n# [server]\n") {
		t.Fatal("scaffoldToml must not teach a repo [server] table")
	}
	if !strings.Contains(scaffoldToml, "[service] endpoint") && !strings.Contains(scaffoldToml, "config.toml") {
		t.Fatal("scaffoldToml must document machine-scope [service] endpoint")
	}
}
