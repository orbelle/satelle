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

	_, log := serve(t, filepath.Join(dir, "ok.log"), cfg,
		func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }, "/story/x")
	if !strings.Contains(log, "\tINFO\tGET\t/story/x\t200\t") {
		t.Errorf("200 request did not log an INFO line: %q", log)
	}

	_, log = serve(t, filepath.Join(dir, "err.log"), cfg,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, "/boom")
	if !strings.Contains(log, "\tERROR\tGET\t/boom\t500\t") {
		t.Errorf("500 request did not log an ERROR line: %q", log)
	}

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
	_, log := serve(t, filepath.Join(dir, "slow.log"), logfile.DefaultConfig,
		func(w http.ResponseWriter, r *http.Request) { time.Sleep(5 * time.Millisecond) }, "/fragment/projects")
	if !strings.Contains(log, "\tWARN\tGET\t/fragment/projects\t") {
		t.Errorf("slow request did not log a WARN line: %q", log)
	}
	_, log = serve(t, filepath.Join(dir, "sse.log"), logfile.DefaultConfig,
		func(w http.ResponseWriter, r *http.Request) { time.Sleep(5 * time.Millisecond) }, "/events")
	if strings.Contains(log, "\tWARN\t") {
		t.Errorf("/events must be exempt from the slow WARN: %q", log)
	}
}

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
		panic(http.ErrAbortHandler)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	srv := httptest.NewServer(RequestLog(mux, logPath, cfg, nil))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(srv.URL + "/events"); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / after an aborted stream failed (connection poisoned?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
}
