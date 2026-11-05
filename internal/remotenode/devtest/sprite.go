// Package devtest holds stand-ins for tests: the fake in-sprite API. Nothing
// in the product imports it.
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

	mu     sync.Mutex
	calls  []SpriteCall
	code   int
	onCall func(SpriteCall)
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
		call := SpriteCall{r.Method, r.URL.Path, string(b), time.Now()}
		f.calls = append(f.calls, call)
		code, hook := f.code, f.onCall
		f.mu.Unlock()
		if hook != nil {
			hook(call)
		}
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

// OnCall runs fn on each later request as it is received, before it is
// answered, so a test can see what else had happened by then.
func (f *SpriteAPI) OnCall(fn func(SpriteCall)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCall = fn
}

// Calls is every request received so far, in order.
func (f *SpriteAPI) Calls() []SpriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SpriteCall(nil), f.calls...)
}
