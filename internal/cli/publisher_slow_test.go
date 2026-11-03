//go:build clislow

package cli

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSlowChangePublisherCLI wires sty_126228b2 + sty_9ba3d709: with [server]
// endpoint, story create delivers change + snapshot before exit; with no
// config the publisher is inert; unreachable/black-holed endpoints do not
// fail the verb and add only bounded latency.
func TestSlowChangePublisherCLI(t *testing.T) {
	var changeHits, snapHits atomic.Int32
	var lastChangeBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ingest/change":
			changeHits.Add(1)
			lastChangeBody = body
		case "/ingest/snapshot":
			snapHits.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	// --- active publisher: both paths arrive before runRoot returns ---
	_ = tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	out, err := runRoot(t, "story", "create",
		"--title", "Push Probe",
		"--body", "emit change event",
		"--acceptance", "1. posted",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	// No sleep: drain is synchronous before exit (sty_9ba3d709 AC1).
	if changeHits.Load() < 1 {
		t.Fatalf("expected ≥1 /ingest/change, got %d", changeHits.Load())
	}
	if snapHits.Load() != 1 {
		t.Fatalf("expected exactly 1 /ingest/snapshot, got %d", snapHits.Load())
	}
	var ev struct {
		RepoKey string `json:"repo_key"`
		Topic   string `json:"topic"`
		Entity  string `json:"entity"`
	}
	if err := json.Unmarshal(lastChangeBody, &ev); err != nil {
		t.Fatalf("event json: %v body=%s", err, lastChangeBody)
	}
	if ev.Topic != "stories" || ev.Entity != "story" || ev.RepoKey == "" {
		t.Errorf("event = %+v", ev)
	}

	// --- inert (explicit none — tempRepo will not clobber a leftover serve URL) ---
	changeHits.Store(0)
	snapHits.Store(0)
	t.Setenv(EnvServerEndpoint, "none")
	repo2 := tempRepo(t)
	out, err = runRoot(t, "story", "create",
		"--title", "No Push",
		"--body", "publisher off",
		"--acceptance", "1. ok",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("create without server: %v\n%s", err, out)
	}
	if changeHits.Load() != 0 || snapHits.Load() != 0 {
		t.Fatalf("inert config made change=%d snap=%d POSTs", changeHits.Load(), snapHits.Load())
	}
	_ = repo2

	// --- fail-silent connection refused ---
	_ = tempRepo(t)
	t.Setenv(EnvServerEndpoint, "http://127.0.0.1:1")
	start := time.Now()
	out, err = runRoot(t, "story", "create",
		"--title", "Blackhole",
		"--body", "dead endpoint",
		"--acceptance", "1. ok",
		"--category", "chore",
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("create with blackhole endpoint must succeed: %v\n%s", err, out)
	}
	// time-subject: the drain budget is the subject. Connection-refused returns
	// instantly; the ceiling is the budget plus headroom for a loaded runner's
	// store open.
	if elapsed > uiDrainBudget+10*time.Second {
		t.Errorf("create took %v with refused endpoint — drain may be blocking", elapsed)
	}
	if !strings.Contains(out, `"id"`) {
		t.Errorf("expected story JSON, got:\n%s", out)
	}

	// --- true black-hole: accept, never respond; budget bounds total latency ---
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open until test ends; never respond.
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	_ = tempRepo(t)
	ep := "http://" + ln.Addr().String()
	t.Setenv(EnvServerEndpoint, ep)
	start = time.Now()
	out, err = runRoot(t, "story", "create",
		"--title", "Hang Probe",
		"--body", "black-holed endpoint",
		"--acceptance", "1. ok",
		"--category", "chore",
	)
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("create with hang endpoint must succeed: %v\n%s", err, out)
	}
	if elapsed > uiDrainBudget+10*time.Second {
		t.Errorf("create took %v with hang endpoint — want ≤ the %v drain budget plus store open", elapsed, uiDrainBudget)
	}
	if !strings.Contains(out, `"id"`) {
		t.Errorf("expected story JSON, got:\n%s", out)
	}
}
