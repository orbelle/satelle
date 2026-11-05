package mirror_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// A snapshot carrying a removed feature's waves is accepted and the waves are
// ignored; story_doc rows merge like the other per-item kinds.
func TestIngestIgnoresWavesAndMergesAttachments(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: s}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	post := func(body string) int {
		resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	full := `{"repo_key":"rk","stories":[{"id":"sto_1"}],"story_docs":[{"id":"sto_1/a.md","item":"sto_1","name":"a.md"}],
		"waves":[{"container":"sto_1","waves":[]}],"docs":[{"kind":"documents","name":"d","body":"x"}]}`
	if code := post(full); code != 200 {
		t.Fatalf("legacy full snapshot: %d", code)
	}
	if n, _ := s.CountItems(t.Context(), "rk", "wave"); n != 0 {
		t.Errorf("waves stored: %d", n)
	}
	delta, _ := json.Marshal(mirror.Snapshot{RepoKey: "rk", MergeKinds: []string{"story_doc"},
		StoryDocs: []json.RawMessage{[]byte(`{"id":"sto_1/b.md","item":"sto_1","name":"b.md"}`)}})
	if code := post(string(delta)); code != 200 {
		t.Fatalf("attachment delta: %d", code)
	}
	if n, _ := s.CountItems(t.Context(), "rk", "story_doc"); n != 2 {
		t.Errorf("story_doc rows = %d, want 2 (merged)", n)
	}
}
