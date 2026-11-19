package provider

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

type SpriteCall struct {
	Method, Path, Body string
	At                 time.Time
}

type SpriteAPI struct {
	Socket string

	mu     sync.Mutex
	calls  []SpriteCall
	code   int
	onCall func(SpriteCall)
}

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

func socketDir(t testing.TB) string {
	dir := t.TempDir()
	if len(dir) < 80 {
		return dir
	}
	base := "/tmp"
	if _, err := os.Stat(base); err != nil {
		base = ""
	}
	dir, err := os.MkdirTemp(base, "sprite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func (f *SpriteAPI) Refuse(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.code = code
}

func (f *SpriteAPI) OnCall(fn func(SpriteCall)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCall = fn
}

func (f *SpriteAPI) Calls() []SpriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SpriteCall(nil), f.calls...)
}
