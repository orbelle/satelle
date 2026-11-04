package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/store"
)

// TestWorkspaceAddPostsTrimmedSnapshotAfterTimeout: when the full snapshot POST
// times out, workspace add posts the light drain snapshot before returning the
// error, so the page still gets the story's new status.
func TestWorkspaceAddPostsTrimmedSnapshotAfterTimeout(t *testing.T) {
	prev := uiSnapshotBudget
	uiSnapshotBudget = 300 * time.Millisecond
	t.Cleanup(func() { uiSnapshotBudget = prev })

	var mu sync.Mutex
	var partial []mirror.Snapshot
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ingest/snapshot" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var snap mirror.Snapshot
		_ = json.Unmarshal(body, &snap)
		if len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0 {
			// A full body stalls past the client's budget.
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		mu.Lock()
		partial = append(partial, snap)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	out, err := runRoot(t, "story", "create", "--title", "Trimmed", "--body", "goal",
		"--acceptance", "1. x", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create: %v out=%s", err, out)
	}
	db, err := store.Open(filepath.Join(config.GlobalDir(), config.RepoKey(repo), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Stories.SetStatus(context.Background(), created.ID, "plan", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	t.Setenv(EnvServerEndpoint, srv.URL)
	out, err = runRoot(t, "workspace", "add")
	if err == nil || !strings.Contains(err.Error(), "posted trimmed snapshot") {
		t.Fatalf("workspace add err = %v, want the full body's timeout naming the trimmed post\n%s", err, out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(partial) != 1 {
		t.Fatalf("partial snapshots received = %d, want 1", len(partial))
	}
	if len(partial[0].Kinds) == 0 {
		t.Fatalf("trimmed snapshot carries no kinds: %+v", partial[0])
	}
	var status string
	for _, raw := range partial[0].Stories {
		var row struct{ ID, Status string }
		if json.Unmarshal(raw, &row) == nil && row.ID == created.ID {
			status = row.Status
		}
	}
	if status != "plan" {
		t.Fatalf("trimmed snapshot story %s status = %q, want plan", created.ID, status)
	}
}
