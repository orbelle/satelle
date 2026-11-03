package devtest

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// SpriteCall is one request the fake sprite API received.
type SpriteCall struct {
	Method, Path, Body string
	At                 time.Time
}

// SpriteAPI is a fake fly-sprite in-sprite API (the Tasks API on
// /.sprite/api.sock), served on a unix socket in a temp dir, that records each
// request and answers it with Code (200 by default).
type SpriteAPI struct {
	Socket string

	mu    sync.Mutex
	calls []SpriteCall
	code  int
}

// NewSpriteAPI serves a fake sprite API until the test ends.
func NewSpriteAPI(t testing.TB) *SpriteAPI {
	t.Helper()
	f := &SpriteAPI{Socket: filepath.Join(socketDir(t), "api.sock"), code: http.StatusOK}
	ln, err := net.Listen("unix", f.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, SpriteCall{r.Method, r.URL.Path, string(b), time.Now()})
		code := f.code
		f.mu.Unlock()
		w.WriteHeader(code)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

// socketDir is a temp dir short enough to bind a unix socket in (sun_path is
// 108 bytes): the test's own, or one under /tmp when a long TMPDIR makes that
// too long.
func socketDir(t testing.TB) string {
	dir := t.TempDir()
	if len(dir) < 80 {
		return dir
	}
	dir, err := os.MkdirTemp("/tmp", "sprite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Refuse makes every later request answer code.
func (f *SpriteAPI) Refuse(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.code = code
}

// Calls is every request received so far, in order.
func (f *SpriteAPI) Calls() []SpriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SpriteCall(nil), f.calls...)
}
