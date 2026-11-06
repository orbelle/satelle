package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/logfile"
)

// serve drives one request through the RequestLog middleware writing to a temp
// server.log, and returns the response recorder plus the log's contents.
func serve(t *testing.T, logPath string, cfg logfile.Config, h http.HandlerFunc, target string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	RequestLog(h, logPath, cfg, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	data, _ := os.ReadFile(logPath)
	return rec, string(data)
}

func TestRequestLogLevels(t *testing.T) {
	cfg := logfile.DefaultConfig
	dir := t.TempDir()

	// 200 → one INFO line carrying method, path, status.
	_, log := serve(t, filepath.Join(dir, "ok.log"), cfg,
		func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }, "/story/x")
	if !strings.Contains(log, "\tINFO\tGET\t/story/x\t200\t") {
		t.Errorf("200 request did not log an INFO line: %q", log)
	}

	// 500 → ERROR line.
	_, log = serve(t, filepath.Join(dir, "err.log"), cfg,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, "/boom")
	if !strings.Contains(log, "\tERROR\tGET\t/boom\t500\t") {
		t.Errorf("500 request did not log an ERROR line: %q", log)
	}

	// panic → recovered, client gets 500, ERROR line with the panic noted.
	rec, log := serve(t, filepath.Join(dir, "panic.log"), cfg,
		func(w http.ResponseWriter, r *http.Request) { panic("kaboom") }, "/panic")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panic did not yield 500, got %d", rec.Code)
	}
	if !strings.Contains(log, "\tERROR\tGET\t/panic\t500\t") || !strings.Contains(log, "panic: kaboom") {
		t.Errorf("panic did not log a recovered ERROR line: %q", log)
	}
}

func TestRequestLogSlowWarn(t *testing.T) {
	old := slowThreshold
	slowThreshold = 1 * time.Millisecond
	defer func() { slowThreshold = old }()

	dir := t.TempDir()
	// A handler slower than the threshold → WARN.
	_, log := serve(t, filepath.Join(dir, "slow.log"), logfile.DefaultConfig,
		func(w http.ResponseWriter, r *http.Request) { time.Sleep(5 * time.Millisecond) }, "/fragment/projects") // time-subject: the handler must outlast the 1ms slow threshold
	if !strings.Contains(log, "\tWARN\tGET\t/fragment/projects\t") {
		t.Errorf("slow request did not log a WARN line: %q", log)
	}
	// /events (SSE) is exempt from the slow WARN even when long-lived.
	_, log = serve(t, filepath.Join(dir, "sse.log"), logfile.DefaultConfig,
		func(w http.ResponseWriter, r *http.Request) { time.Sleep(5 * time.Millisecond) }, "/events") // time-subject: slower than the 1ms threshold, yet exempt as /events
	if strings.Contains(log, "\tWARN\t") {
		t.Errorf("/events must be exempt from the slow WARN: %q", log)
	}
}

// TestRequestLogFlusherPassthrough guards the SSE / reverse-proxy seam: the
// wrapped ResponseWriter the handler sees MUST satisfy http.Flusher.
func TestRequestLogFlusherPassthrough(t *testing.T) {
	var sawFlusher bool
	serve(t, filepath.Join(t.TempDir(), "f.log"), logfile.DefaultConfig,
		func(w http.ResponseWriter, r *http.Request) {
			_, sawFlusher = w.(http.Flusher)
		}, "/events")
	if !sawFlusher {
		t.Error("handler's ResponseWriter must satisfy http.Flusher through the wrapper (SSE/proxy depend on it)")
	}
}

// TestRequestLogRotationWiring proves the middleware writes through
// logfile.Append's rotation: a tiny size cap forces a rolled server-*.log sibling.
func TestRequestLogRotationWiring(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")
	cfg := logfile.Config{MaxSizeBytes: 200, MaxFiles: 5}
	for i := 0; i < 20; i++ {
		serve(t, logPath, cfg, func(w http.ResponseWriter, r *http.Request) {}, "/story/some-longish-path")
	}
	entries, _ := os.ReadDir(dir)
	var rolled int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "server-") && strings.HasSuffix(e.Name(), ".log") {
			rolled++
		}
	}
	if rolled == 0 {
		t.Errorf("expected at least one rotated server-*.log; dir has %d entries", len(entries))
	}
}

// TestRequestLogAbortHandlerRepanics asserts http.ErrAbortHandler propagates
// (is NOT recovered) and emits no ERROR line — the fix that stops an aborted SSE
// stream from poisoning a keep-alive connection (sty_fa24469a).
func TestRequestLogAbortHandlerRepanics(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "abort.log")
	h := RequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }),
		logPath, logfile.DefaultConfig, nil)

	func() {
		defer func() {
			rec := recover()
			if rec != http.ErrAbortHandler {
				t.Fatalf("expected http.ErrAbortHandler to propagate, got %v", rec)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/satelle/events", nil))
		t.Fatal("ServeHTTP should have propagated the abort panic")
	}()

	if data, _ := os.ReadFile(logPath); strings.Contains(string(data), "ERROR") {
		t.Errorf("aborted stream should not log an ERROR line, got:\n%s", data)
	}
}

// TestRequestLogAbortClosesConnection is the connection-reuse regression: a real
// server whose /events aborts mid-stream must not poison the keep-alive
// connection — a following GET / on the same client must still succeed quickly.
func TestRequestLogAbortClosesConnection(t *testing.T) {
	cfg := logfile.DefaultConfig
	logPath := filepath.Join(t.TempDir(), "server.log")
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": open\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler) // mimic the reverse-proxy backend aborting the stream
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	srv := httptest.NewServer(RequestLog(mux, logPath, cfg, nil))
	defer srv.Close()

	// One client → one keep-alive transport shared across both requests.
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(srv.URL + "/events"); err == nil {
		io.Copy(io.Discard, resp.Body) // drain until the abort surfaces
		resp.Body.Close()
	}
	// The reused/replacement connection must serve / promptly (pre-fix: hangs).
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / after an aborted stream failed (connection poisoned?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
}
