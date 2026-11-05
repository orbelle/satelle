package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
