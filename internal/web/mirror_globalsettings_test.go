package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestGlobalSettingsGETAndLoopbackPOST(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ms := NewMirror(s)

	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/settings/global", nil))
	if rr.Code != 200 {
		t.Fatalf("GET status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="g-server"`) {
		t.Fatalf("GET missing hosted server field:\n%s", rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/settings/global", strings.NewReader(url.Values{"server": {"https://from-ui.dev/"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Satelle-Settings", "1")
	req.RemoteAddr = "127.0.0.1:4321"
	rr = httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("loopback POST status %d body=%s", rr.Code, rr.Body.String())
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if gc.Hosted.ResolveServer() != "https://from-ui.dev" {
		t.Fatalf("saved %q", gc.Hosted.Server)
	}

	rr = httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/settings/global", nil))
	if !strings.Contains(rr.Body.String(), "https://from-ui.dev") {
		t.Fatalf("GET after POST missing saved URL:\n%s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/global", strings.NewReader(url.Values{"server": {""}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Satelle-Settings", "1")
	req.RemoteAddr = "127.0.0.1:4321"
	rr = httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("clear POST status %d", rr.Code)
	}
	gc, err = config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if gc.Hosted.Server != "" {
		t.Fatalf("clear left server = %q", gc.Hosted.Server)
	}
	raw, err := os.ReadFile(config.GlobalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `server = ""`) {
		t.Fatalf("cleared file must keep an empty server key, not resurrect a default:\n%s", raw)
	}
}

func TestGlobalSettingsAcceptsMultipartFormData(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ms := NewMirror(s)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("server", "https://from-formdata.dev/"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/global", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Satelle-Settings", "1")
	req.RemoteAddr = "127.0.0.1:4321"
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("multipart POST status %d body=%s", rr.Code, rr.Body.String())
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if gc.Hosted.ResolveServer() != "https://from-formdata.dev" {
		t.Fatalf("multipart saved server %q", gc.Hosted.Server)
	}
	if gc.UI.Theme != "dark" {
		t.Fatalf("multipart saved theme %q", gc.UI.Theme)
	}
}

func TestGlobalSettingsRejectsInvalidURL(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ms := NewMirror(s)
	req := httptest.NewRequest(http.MethodPost, "/settings/global", strings.NewReader(url.Values{"server": {"not-a-url"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:4321"
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 body=%s", rr.Code, rr.Body.String())
	}
}

func TestGlobalSettingsRejectsNonLoopback(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ms := NewMirror(s)
	if err := config.SaveGlobalHostedServer("https://keep.dev"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/settings/global", strings.NewReader(url.Values{"server": {"https://evil.dev"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.9:9"
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rr.Code)
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if gc.Hosted.ResolveServer() != "https://keep.dev" {
		t.Fatalf("non-loopback write landed: %q", gc.Hosted.Server)
	}
}

func TestProjectSettingsLinksGlobalSettings(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.TouchPartition(t.Context(), "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/r/demo/settings", nil))
	if !strings.Contains(rr.Body.String(), `href="/settings/global"`) {
		t.Fatalf("project settings missing global settings link:\n%s", rr.Body.String())
	}
}
