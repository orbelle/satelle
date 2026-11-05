package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestNewHostedClientBoundSendsHeaderAndRegisters(t *testing.T) {
	testutil.IsolateHome(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	hosted.LocationStatePathOverride = filepath.Join(xdg, "satelle", "location-state.json")
	t.Cleanup(func() { hosted.LocationStatePathOverride = "" })

	var gotHeader, gotID string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/locations", func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(hosted.LocationHeader)
		var in struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		gotID = in.ID
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": in.ID})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	store := hosted.FileStore{}
	_ = store.Save(hosted.Credential{ServerURL: ts.URL, AccessToken: "tok", RefreshToken: "r"})

	repo := t.TempDir()
	c := newHostedClient(context.Background(), ts.URL, repo)
	if c.Location() == "" {
		t.Fatal("bound client has empty location")
	}
	if gotID == "" || gotHeader != gotID || gotID != c.Location() {
		t.Fatalf("register id=%q header=%q client=%q", gotID, gotHeader, c.Location())
	}
}

func TestNewHostedClientEmptyRepoDoesNotWriteState(t *testing.T) {
	testutil.IsolateHome(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "satelle", "location-state.json")
	hosted.LocationStatePathOverride = path
	t.Cleanup(func() { hosted.LocationStatePathOverride = "" })

	c := newHostedClient(context.Background(), "https://example.invalid", "")
	if c.Location() != "" {
		t.Fatalf("empty repoRoot attached location %q", c.Location())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file created for empty repoRoot: %v", err)
	}
}
