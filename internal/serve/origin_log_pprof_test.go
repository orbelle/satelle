package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// AC2 (sty_0367d20c): satelled's reseed child runs with the hosted ingest
// origin in its environment, so the `workspace add` drain never notifies the
// Pusher. Drives the real reseedViaCLI against the fake satelle.
func TestReseedChildCarriesTheHostedIngestOrigin(t *testing.T) {
	newFakeCLI(t)
	origins := filepath.Join(t.TempDir(), "origins")
	t.Setenv("SATELLE_TEST_ORIGINS", origins)
	t.Setenv(mirror.EnvIngestOrigin, "") // not inherited from the test's own env
	if err := reseedViaCLI(context.Background(), t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(origins)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != string(mirror.OriginHosted) {
		t.Fatalf("reseed child's %s = %q, want %q", mirror.EnvIngestOrigin, got, mirror.OriginHosted)
	}
	// The push child keeps satelled's environment: its origin comes from the
	// verb's own annotation, which internal/cli proves.
	if err := os.Remove(origins); err != nil {
		t.Fatal(err)
	}
	if err := pushViaCLI(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(origins); strings.TrimSpace(string(raw)) != "local" {
		t.Fatalf("push child's env origin = %q, want unset", raw)
	}
}

// AC2: a hosted-origin ingest — every write a sync child makes — leaves the
// Pusher clean, so it schedules no push.
func TestHostedOriginIngestLeavesThePusherClean(t *testing.T) {
	p := &Pusher{}
	p.OnIngest("rk", mirror.OriginHosted)
	if n := p.dirtyCount(); n != 0 {
		t.Fatalf("dirty = %d after a hosted-origin ingest, want 0", n)
	}
	p.OnIngest("rk", mirror.OriginLocal)
	if n := p.dirtyCount(); n != 1 {
		t.Fatalf("dirty = %d after a local ingest, want 1", n)
	}
}

// AC4 (sty_0367d20c): every push run writes one line to satelled's server log
// with its outcome and when it runs next — the repeats the transition lines
// suppress included.
func TestEverySyncRunIsLoggedToTheServerLog(t *testing.T) {
	serverLog := mirror.ServerLogPath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(serverLog), 0o755); err != nil {
		t.Fatal(err)
	}
	logLine := newLogLine(serverLog, nil)
	p := &Pusher{
		Log:     logLine,
		Resolve: func(context.Context, string) (string, bool) { return "/repo/one", true },
		Push:    func(context.Context, string) error { return errors.New("refused") },
	}
	for i := 0; i < 3; i++ {
		p.backoff = nil // let each flush run its push, as three backoffs ending would
		p.Notify("rk")
		p.flush(context.Background())
	}
	r := &Reconciler{Log: logLine, Targets: func(context.Context) []string { return []string{"/repo/one"} },
		Reseed: func(context.Context, string) error { return nil }}
	r.pass(context.Background())

	raw, err := os.ReadFile(serverLog)
	if err != nil {
		t.Fatal(err)
	}
	var push, reconcile int
	for _, ln := range strings.Split(string(raw), "\n") {
		switch {
		case strings.Contains(ln, "sync push /repo/one: error next in "):
			push++
		case strings.Contains(ln, "sync reseed /repo/one: ok next in "):
			reconcile++
		}
	}
	if push != 3 {
		t.Fatalf("server log has %d `sync push … next in` lines, want 3:\n%s", push, raw)
	}
	if reconcile != 1 {
		t.Fatalf("server log has %d reconcile run lines, want 1:\n%s", reconcile, raw)
	}
}

// AC4: /debug/pprof is served to a loopback client and is a 404 for any other,
// whatever address satelled listens on; other paths reach the UI handler.
func TestPprofServedToLoopbackOnly(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ui")) })
	h := pprofLocal(ui)
	for _, c := range []struct {
		remote, path string
		want         int
	}{
		{"127.0.0.1:1", "/debug/pprof/", http.StatusOK},
		{"[::1]:1", "/debug/pprof/", http.StatusOK},
		{"10.0.0.5:1", "/debug/pprof/", http.StatusNotFound},
		{"10.0.0.5:1", "/debug/pprof/heap", http.StatusNotFound},
		{"10.0.0.5:1", "/", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.RemoteAddr = c.remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.remote, c.path, rec.Code, c.want)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("loopback /debug/pprof/ is not the pprof index: %.200s", rec.Body.String())
	}
}
