//go:build linux

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

const (
	goldenSVC     = "[Unit]\nDescription=satelled (satelle local web UI)\nAfter=network.target\n\n[Service]\nExecStart=%BIN% --addr %ADDR% --port %PORT%\nRestart=always\nRestartSec=2\n\n[Install]\nWantedBy=default.target\n"
	goldenSYNC    = "[Unit]\nDescription=satelle ledger sync pass (push then pull each project on this machine)\nAfter=network-online.target\n\n[Service]\nType=oneshot\nExecStart=%CLI% ledger autosync\nTimeoutStartSec=300\n"
	goldenTIMER   = "[Unit]\nDescription=satelle ledger sync every 30 s\n\n[Timer]\nOnBootSec=30s\nOnUnitInactiveSec=30s\nAccuracySec=1s\nUnit=satelle-sync.service\n\n[Install]\nWantedBy=timers.target\n"
	goldenENABLED = "satelle.service enabled and (re)started\n"
	goldenTIMERON = "satelle-sync.timer enabled and started: each project on this machine syncs every 30s\n"
)

var (
	goldenApplyD  = []string{"--user show-environment", "--user daemon-reload", "--user enable satelle.service", "--user restart satelle.service", "--user enable --now satelle-sync.timer"}
	goldenApplyND = []string{"--user show-environment", "--user daemon-reload", "--user enable --now satelle-sync.timer"}
	goldenStatusC = []string{"--user is-active satelle.service", "--user is-active satelle-sync.timer"}
)

func goldenSvc(bin, addr, port string) string {
	return strings.NewReplacer("%BIN%", bin, "%ADDR%", addr, "%PORT%", port).Replace(goldenSVC)
}

type goldenEnv struct {
	home, cfg, sat, cli, log string
	u, ss, tm                string
}

func (g *goldenEnv) statusO() string {
	return "unit      " + g.u + " (unknown)\n" +
		"sync      satelle-sync.timer (unknown)\n" +
		"config    " + g.cfg + " (port 1, addr 0.0.0.0)\n" +
		"satelled  not answering at http://127.0.0.1:1/healthz\n"
}

func (g *goldenEnv) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(g.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (g *goldenEnv) reset(t *testing.T) {
	t.Helper()
	if err := os.Remove(g.log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func (g *goldenEnv) run(t *testing.T, args ...string) (string, []string) {
	t.Helper()
	g.reset(t)
	out, _ := run(t, "", args...)
	return out, g.calls(t)
}

func (g *goldenEnv) putDaemon(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	g.sat = filepath.Join(dir, "satelled")
	writeExec(t, g.sat, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newGoldenEnv(t *testing.T) *goldenEnv {
	t.Helper()
	isolate(t)
	var keep []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "satelled")); err == nil {
			continue
		}
		keep = append(keep, d)
	}
	t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
	if p, err := exec.LookPath("satelled"); err == nil {
		t.Fatalf("satelled still on PATH at %s", p)
	}
	g := &goldenEnv{home: os.Getenv("HOME")}
	g.log = fakeSystemctl(t, "")
	g.cfg = config.GlobalConfigPath()
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	g.cli = cli
	unitDir := filepath.Join(g.home, ".config", "systemd", "user")
	g.u = filepath.Join(unitDir, "satelle.service")
	g.ss = filepath.Join(unitDir, "satelle-sync.service")
	g.tm = filepath.Join(unitDir, "satelle-sync.timer")
	return g
}

func (g *goldenEnv) install(t *testing.T) {
	t.Helper()
	out, calls := g.run(t, "service", "install", "--satelled", "/x/satelled")
	want := "wrote " + g.u + "\nwrote " + g.ss + "\nwrote " + g.tm + "\n" + goldenENABLED + goldenTIMERON
	if out != want {
		t.Fatalf("install output:\n%q\nwant\n%q", out, want)
	}
	if !slices.Equal(calls, goldenApplyD) {
		t.Fatalf("install calls %q, want %q", calls, goldenApplyD)
	}
}

func goldenFile(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Errorf("%s:\n%q\nwant\n%q", p, b, want)
	}
}

func goldenEq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n%q\nwant\n%q", what, got, want)
	}
}

func goldenCalls(t *testing.T, what string, got []string, want ...[]string) {
	t.Helper()
	var all []string
	for _, w := range want {
		all = append(all, w...)
	}
	if !slices.Equal(got, all) {
		t.Errorf("%s calls:\n%q\nwant\n%q", what, got, all)
	}
}

func goldenDoctor(t *testing.T, g *goldenEnv, args ...string) (doctorReport, []string) {
	t.Helper()
	out, calls := g.run(t, append([]string{"doctor", "--json"}, args...)...)
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	return rep, calls
}

func serviceIssues(rep doctorReport) []doctorIssue {
	var out []doctorIssue
	for _, is := range rep.Issues {
		if is.Check != "versions" && is.Check != "sync" {
			continue
		}
		if is.Resolve != resolveInstallService && is.Resolve != resolveRestartService &&
			!strings.Contains(is.Problem, "satelle.service") && !strings.Contains(is.Problem, "satelled is installed") &&
			!strings.Contains(is.Problem, "satelle-sync.timer") {
			continue
		}
		out = append(out, is)
	}
	return out
}

func TestLinuxServiceGolden(t *testing.T) {
	t.Run("units", func(t *testing.T) {
		goldenEq(t, "renderUnit", renderUnit("/x/satelled", "127.0.0.1", 8787), goldenSvc("/x/satelled", "127.0.0.1", "8787"))
		goldenEq(t, "renderSyncService", renderSyncService("/x/satelle"), strings.ReplaceAll(goldenSYNC, "%CLI%", "/x/satelle"))
		goldenEq(t, "renderSyncTimer", renderSyncTimer(), goldenTIMER)
	})
	t.Run("install", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		goldenFile(t, g.u, goldenSvc("/x/satelled", "0.0.0.0", "1"))
		goldenFile(t, g.ss, strings.ReplaceAll(goldenSYNC, "%CLI%", g.cli))
		goldenFile(t, g.tm, goldenTIMER)
	})
	t.Run("status", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		out, calls := g.run(t, "service", "status")
		goldenEq(t, "status", out, g.statusO())
		goldenCalls(t, "status", calls, goldenStatusC)
	})
	t.Run("refresh, all kept", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		g.putDaemon(t)
		writeFile(t, g.u, goldenSvc(g.sat, "0.0.0.0", "1"))
		out, calls := g.run(t, "service", "refresh")
		goldenEq(t, "refresh", out, "kept "+g.u+"\nkept "+g.ss+"\nkept "+g.tm+"\n"+goldenENABLED+goldenTIMERON+g.statusO())
		goldenCalls(t, "refresh", calls, goldenApplyD, goldenStatusC)
	})
	t.Run("refresh, missing and repointed", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		g.putDaemon(t)
		if err := os.Remove(g.tm); err != nil {
			t.Fatal(err)
		}
		writeFile(t, g.u, goldenSvc("/old/satelled", "0.0.0.0", "1"))
		out, calls := g.run(t, "service", "refresh")
		goldenEq(t, "refresh", out, "wrote "+g.u+" (was /old/satelled)\nkept "+g.ss+"\nwrote "+g.tm+" (missing)\n"+goldenENABLED+goldenTIMERON+g.statusO())
		goldenCalls(t, "refresh", calls, goldenApplyD, goldenStatusC)
	})
	t.Run("refresh, no satelled and no unit", func(t *testing.T) {
		g := newGoldenEnv(t)
		out, calls := g.run(t, "service", "refresh")
		goldenEq(t, "refresh", out, "satelled: no binary beside satelle or on PATH; its unit was not installed\nwrote "+g.ss+" (missing)\nwrote "+g.tm+" (missing)\n"+goldenTIMERON+g.statusO())
		goldenCalls(t, "refresh", calls, goldenApplyND, goldenStatusC)
	})
	t.Run("refresh, existing unreadable unit", func(t *testing.T) {
		g := newGoldenEnv(t)
		if err := os.MkdirAll(g.u, 0o755); err != nil {
			t.Fatal(err)
		}
		out, calls := g.run(t, "service", "refresh")
		goldenEq(t, "refresh", out, "wrote "+g.ss+" (missing)\nwrote "+g.tm+" (missing)\n"+goldenENABLED+goldenTIMERON+g.statusO())
		goldenCalls(t, "refresh", calls, goldenApplyD, goldenStatusC)
		issues, notes := checkVersions(t.Context(), nil)
		if len(issues) != 0 || !slices.Equal(notes, []string{"versions: no satelled unit or binary is installed on this machine (satelle service install)"}) {
			t.Errorf("checkVersions = %+v, %q", issues, notes)
		}
	})
	t.Run("restart", func(t *testing.T) {
		g := newGoldenEnv(t)
		out, calls := g.run(t, "service", "restart")
		goldenEq(t, "restart", out, "satelle.service restarted\n")
		goldenCalls(t, "restart", calls, []string{"--user restart satelle.service"})
	})
	t.Run("uninstall", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		out, calls := g.run(t, "service", "uninstall")
		goldenEq(t, "uninstall", out, "removed "+g.tm+"\nremoved "+g.ss+"\nremoved "+g.u+"\n")
		goldenCalls(t, "uninstall", calls, []string{"--user disable --now satelle-sync.timer", "--user disable --now satelle.service", "--user daemon-reload"})
	})
	t.Run("doctor, nothing installed", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.putDaemon(t)
		rep, calls := goldenDoctor(t, g)
		want := []doctorIssue{{Check: "versions", Problem: "satelle.service is not installed, so satelled (" + g.sat + ") does not run", Fix: "satelle service install", Resolve: resolveInstallService}}
		if got := serviceIssues(rep); !slices.Equal(got, want) {
			t.Errorf("issues %+v, want %+v", got, want)
		}
		for _, n := range rep.Notes {
			if strings.HasPrefix(n, "versions: ") || strings.HasPrefix(n, "sync: ") {
				t.Errorf("note %q", n)
			}
		}
		goldenCalls(t, "doctor", calls)
	})
	t.Run("doctor, installed, timer present", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		rep, calls := goldenDoctor(t, g)
		want := []doctorIssue{
			{Check: "versions", Problem: "satelled is installed but not answering at http://127.0.0.1:1/healthz", Fix: "satelle service restart", Resolve: resolveRestartService},
			{Check: "sync", Problem: "satelle-sync.timer is unknown", Fix: "satelle service install", Resolve: resolveInstallService},
		}
		if got := serviceIssues(rep); !slices.Equal(got, want) {
			t.Errorf("issues %+v, want %+v", got, want)
		}
		goldenCalls(t, "doctor", calls, []string{"--user is-active satelle-sync.timer"})
	})
	t.Run("doctor, installed, timer absent", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		if err := os.Remove(g.tm); err != nil {
			t.Fatal(err)
		}
		rep, calls := goldenDoctor(t, g)
		var sync []doctorIssue
		for _, is := range rep.Issues {
			if is.Check == "sync" {
				sync = append(sync, is)
			}
		}
		want := []doctorIssue{{Check: "sync", Problem: "satelle-sync.timer is not installed, so nothing replicates this repo's ledger on its own", Fix: "satelle service install", Resolve: resolveInstallService}}
		if !slices.Equal(sync, want) {
			t.Errorf("sync issues %+v, want %+v", sync, want)
		}
		goldenCalls(t, "doctor", calls)
	})
	t.Run("doctor --resolve, nothing installed", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.putDaemon(t)
		out, calls := g.run(t, "doctor", "--resolve", "--json")
		var rep resolveReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("doctor --resolve --json: %v\n%s", err, out)
		}
		problem := "satelle.service is not installed, so satelled (" + g.sat + ") does not run"
		if !slices.ContainsFunc(rep.Actions, func(a resolveAction) bool {
			return a.Check == "versions" && a.Problem == problem && a.Result == "applied"
		}) {
			t.Errorf("actions %+v lack %q applied", rep.Actions, problem)
		}
		goldenCalls(t, "doctor --resolve", calls, goldenApplyD)
	})
	t.Run("doctor --resolve, timer unknown", func(t *testing.T) {
		g := newGoldenEnv(t)
		g.install(t)
		_, calls := g.run(t, "doctor", "--resolve", "--json")
		goldenCalls(t, "doctor --resolve", calls, []string{"--user is-active satelle-sync.timer"}, goldenApplyD)
	})
}
