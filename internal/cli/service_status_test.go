package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

func healthz(t *testing.T, body func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { body(w) }))
	t.Cleanup(srv.Close)
	return srv.URL + "/healthz"
}

func serving(info buildinfo.Info) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			buildinfo.Info
		}{"ok", info})
	}
}

var running = buildinfo.Info{Name: "satelled", Version: "1.0.0", Commit: "aaaaaaaaaaaa", BuildTime: "2026-11-06T09:00:00Z"}

func report(t *testing.T, ep, installed string) string {
	t.Helper()
	var out bytes.Buffer
	reportDaemon(&out, http.DefaultClient, ep, installed, "/x/satelled")
	return out.String()
}

func TestReportDaemonMatch(t *testing.T) {
	out := report(t, healthz(t, serving(running)), running.String())
	if !strings.Contains(out, "running   "+running.String()) || strings.Contains(out, "warning") {
		t.Fatalf("a matching daemon must report its line and no warning:\n%s", out)
	}
}

func TestReportDaemonDiffers(t *testing.T) {
	installed := buildinfo.Info{Name: "satelled", Version: "1.0.1", Commit: "bbbbbbbbbbbb", BuildTime: "2026-11-06T10:00:00Z"}.String()
	out := report(t, healthz(t, serving(running)), installed)
	if !strings.Contains(out, "warning") || !strings.Contains(out, running.String()) || !strings.Contains(out, installed) || !strings.Contains(out, "satelle service restart") {
		t.Fatalf("a differing daemon must warn naming both and the restart:\n%s", out)
	}
}

func TestReportDaemonLegacyBody(t *testing.T) {
	out := report(t, healthz(t, func(w http.ResponseWriter) { fmt.Fprintln(w, "ok") }), running.String())
	if !strings.Contains(out, "unknown") || !strings.Contains(out, "warning") {
		t.Fatalf("a satelled with no identity on /healthz is unknown and warned:\n%s", out)
	}
}

func TestReportDaemonDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	ep := srv.URL + "/healthz"
	srv.Close()
	if out := report(t, ep, running.String()); !strings.Contains(out, "not answering") {
		t.Fatalf("a closed port must report not answering:\n%s", out)
	}
}

func TestUnitDaemonRoundTrip(t *testing.T) {
	if got := unitDaemon(renderUnit("/x/satelled", "127.0.0.1", 8787)); got != "/x/satelled" {
		t.Errorf("unitDaemon(renderUnit) = %q", got)
	}
	if got := unitDaemon(""); got != "" {
		t.Errorf("unitDaemon(\"\") = %q", got)
	}
}
