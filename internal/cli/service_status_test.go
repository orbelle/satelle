package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	return reportWith(t, ep, installed, "")
}

func reportWith(t *testing.T, ep, installed, cli string) string {
	t.Helper()
	var out bytes.Buffer
	reportDaemon(&out, http.DefaultClient, ep, installed, "/x/satelled", cli)
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

// A CLI whose version differs from the installed satelled's is a half-done
// update: status names satelle update.
func TestReportDaemonCLIDiffers(t *testing.T) {
	cli := buildinfo.Info{Name: "satelle", Version: "1.0.1", Commit: "cccccccccccc", BuildTime: "2026-11-06T10:00:00Z"}.String()
	out := reportWith(t, healthz(t, serving(running)), running.String(), cli)
	if !strings.Contains(out, "satelle   "+cli) || !strings.Contains(out, "run 'satelle update'") || strings.Contains(out, "satelle service restart") {
		t.Fatalf("a differing CLI must be reported and warned with satelle update only:\n%s", out)
	}
}

// The card's case (sto_49b6e762): satelled 1.0.0 still runs after update
// installed 1.0.1 of both. Only the restart is named.
func TestReportDaemonCardCase(t *testing.T) {
	installed := buildinfo.Info{Name: "satelled", Version: "1.0.1", Commit: "bbbbbbbbbbbb", BuildTime: "2026-11-06T10:00:00Z"}.String()
	cli := buildinfo.Info{Name: "satelle", Version: "1.0.1", Commit: "bbbbbbbbbbbb", BuildTime: "2026-11-06T10:00:00Z"}.String()
	out := reportWith(t, healthz(t, serving(running)), installed, cli)
	if strings.Count(out, "warning") != 1 || !strings.Contains(out, "run 'satelle service restart'") || strings.Contains(out, "satelle update") {
		t.Fatalf("the card's case must warn restart only:\n%s", out)
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

// The sync timer runs the pass 30 s after the last one ended, to the second:
// a write reaches another machine within two periods plus its passes, the
// 90-second bound (sto_5c706973).
func TestServiceRendersSyncTimer(t *testing.T) {
	timer := renderSyncTimer()
	for _, want := range []string{"OnBootSec=30s", "OnUnitInactiveSec=30s", "AccuracySec=1s", "Unit=" + syncServiceName, "WantedBy=timers.target"} {
		if !strings.Contains(timer, want+"\n") {
			t.Errorf("timer lacks %s:\n%s", want, timer)
		}
	}
	svc := renderSyncService("/x/satelle")
	if !strings.Contains(svc, "Type=oneshot\n") || !strings.Contains(svc, "TimeoutStartSec=300\n") {
		t.Errorf("service:\n%s", svc)
	}
	var exec string
	for _, line := range strings.Split(svc, "\n") {
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			exec = rest
		}
	}
	if exec != "/x/satelle ledger autosync" {
		t.Errorf("ExecStart = %q, want the CLI's ledger autosync", exec)
	}
	if !strings.HasSuffix(exec, "ledger autosync") {
		t.Errorf("ExecStart %q does not end with ledger autosync", exec)
	}
}

// service install writes satelled's unit and the sync service and timer, and
// enables and starts the timer (sto_5c706973).
func TestInstallWritesSyncUnits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the units are systemd-only")
	}
	isolate(t)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "systemctl.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	must(t, "service", "install", "--satelled", "/x/satelled")
	dir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	for _, name := range []string{serviceUnitName, syncServiceName, syncTimerName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	timer, _ := os.ReadFile(filepath.Join(dir, syncTimerName))
	if string(timer) != renderSyncTimer() {
		t.Errorf("timer file:\n%s", timer)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "--user enable --now "+syncTimerName+"\n") {
		t.Errorf("the timer was not enabled with --now:\n%s", calls)
	}
	must(t, "service", "uninstall")
	for _, name := range []string{serviceUnitName, syncServiceName, syncTimerName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s not removed: %v", name, err)
		}
	}
}

// A second refresh finds every unit naming the binaries it would write, and
// keeps all three (sto_1b6a7ed3).
func TestServiceRefreshKeepsMatchingUnits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the units are systemd-only")
	}
	isolate(t)
	t.Setenv(envNoService, "")
	fakeSystemctl(t, "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "satelled"), []byte("#!/bin/sh\necho 'satelled 0.1.0 (commit x, built y)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first := must(t, "service", "refresh")
	if strings.Count(first, "(missing)") != 3 {
		t.Fatalf("first refresh:\n%s", first)
	}
	second := must(t, "service", "refresh")
	if strings.Count(second, "kept ") != 3 || strings.Contains(second, "wrote ") {
		t.Errorf("second refresh rewrote a unit:\n%s", second)
	}
}
