//go:build darwin

package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

type launchdEnv struct {
	home, uid, dp, sp, log string
	port                   int
}

func newLaunchdEnv(t *testing.T, opt launchctlFake) *launchdEnv {
	t.Helper()
	isolate(t)
	e := &launchdEnv{home: os.Getenv("HOME"), uid: strconv.Itoa(os.Getuid())}
	e.dp = filepath.Join(e.home, "Library", "LaunchAgents", "dev.satelle.satelled.plist")
	e.sp = filepath.Join(e.home, "Library", "LaunchAgents", "dev.satelle.sync.plist")
	e.log = fakeLaunchctl(t, opt)
	return e
}

func (e *launchdEnv) setPort(t *testing.T, port int) {
	t.Helper()
	e.port = port
	writeFile(t, filepath.Join(config.GlobalDir(), config.GlobalConfigName), fmt.Sprintf("[service]\nport = %d\n", port))
}

func (e *launchdEnv) listen(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	e.setPort(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	return srv
}

func (e *launchdEnv) fakeDaemon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "satelled")
	writeExec(t, p, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}

func (e *launchdEnv) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func (e *launchdEnv) clear(t *testing.T) {
	t.Helper()
	if err := os.Remove(e.log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func (e *launchdEnv) gui() string { return "gui/" + e.uid }

func (e *launchdEnv) target(label string) string { return e.gui() + "/" + label }

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, "", args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func TestServiceInstallLoadsLaunchAgents(t *testing.T) {
	e := newLaunchdEnv(t, launchctlFake{})
	out := mustRun(t, "service", "install", "--satelled", "/x/satelled", "--port", "18787")
	d, s := readText(t, e.dp), readText(t, e.sp)
	for _, want := range []string{"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>"} {
		if !strings.Contains(d, want) {
			t.Errorf("satelled plist lacks %q:\n%s", want, d)
		}
	}
	if !inOrder(d, "<string>/x/satelled</string>", "<string>--addr</string>", "<string>0.0.0.0</string>", "<string>--port</string>", "<string>18787</string>") {
		t.Errorf("satelled plist arguments:\n%s", d)
	}
	cli, _ := os.Executable()
	if !inOrder(s, "<string>"+plistText(cli)+"</string>", "<string>ledger</string>", "<string>autosync</string>") ||
		!strings.Contains(s, "<key>StartInterval</key>\n\t<integer>30</integer>") {
		t.Errorf("sync plist:\n%s", s)
	}
	for _, p := range []string{e.dp, e.sp} {
		if lint, err := exec.Command("/usr/bin/plutil", "-lint", p).CombinedOutput(); err != nil {
			t.Errorf("plutil -lint %s: %v\n%s", p, err, lint)
		}
	}
	if log := e.calls(t); !inOrder(log, "bootstrap "+e.gui()+" "+e.dp, "bootstrap "+e.gui()+" "+e.sp) {
		t.Errorf("launchctl calls:\n%s", log)
	}
	if !strings.Contains(out, "dev.satelle.satelled loaded and (re)started\n") || !strings.Contains(out, "dev.satelle.sync loaded: each project on this machine syncs every 30s\n") {
		t.Errorf("install output:\n%s", out)
	}
	fp := launchAgentPath(followLabel)
	f := readText(t, fp)
	if !inOrder(f, "<string>"+plistText(cli)+"</string>", "<string>ledger</string>", "<string>follow</string>") ||
		!strings.Contains(f, "<key>KeepAlive</key>\n\t<true/>") {
		t.Errorf("follow plist:\n%s", f)
	}
	if log := e.calls(t); !strings.Contains(log, "bootstrap "+e.gui()+" "+fp) {
		t.Errorf("the follow agent was not loaded:\n%s", log)
	}
	if !strings.Contains(out, "dev.satelle.follow loaded and (re)started: writes push and other locations' changes pull within seconds\n") {
		t.Errorf("install output:\n%s", out)
	}
}

func TestServiceRefreshRepairsOneLaunchAgent(t *testing.T) {
	setup := func(t *testing.T) (*launchdEnv, string) {
		e := newLaunchdEnv(t, launchctlFake{})
		e.setPort(t, 1)
		sat := e.fakeDaemon(t)
		mustRun(t, "service", "refresh")
		if !exists(e.dp) || !exists(e.sp) {
			t.Fatal("the first refresh did not write both plists")
		}
		return e, sat
	}
	t.Run("sync plist missing", func(t *testing.T) {
		e, _ := setup(t)
		before := readText(t, e.dp)
		if err := os.Remove(e.sp); err != nil {
			t.Fatal(err)
		}
		e.clear(t)
		out := mustRun(t, "service", "refresh")
		if !strings.Contains(out, "wrote "+e.sp+" (missing)\n") || !strings.Contains(out, "kept "+e.dp+"\n") {
			t.Errorf("refresh output:\n%s", out)
		}
		if readText(t, e.dp) != before {
			t.Error("the kept satelled plist changed")
		}
		log := e.calls(t)
		if !strings.Contains(log, "kickstart -k "+e.target(daemonLabel)) || !strings.Contains(log, "bootstrap "+e.gui()+" "+e.sp) ||
			strings.Contains(log, "bootout "+e.target(daemonLabel)) {
			t.Errorf("launchctl calls:\n%s", log)
		}
	})
	t.Run("satelled plist names another binary", func(t *testing.T) {
		e, sat := setup(t)
		before := readText(t, e.sp)
		writeFile(t, e.dp, renderDaemonPlist("/old/satelled", "0.0.0.0", 1))
		e.clear(t)
		out := mustRun(t, "service", "refresh")
		if !strings.Contains(out, "wrote "+e.dp+" (was /old/satelled)\n") || !strings.Contains(out, "kept "+e.sp+"\n") {
			t.Errorf("refresh output:\n%s", out)
		}
		if got := plistProgram(readText(t, e.dp)); got != sat {
			t.Errorf("satelled plist names %q, want %q", got, sat)
		}
		if readText(t, e.sp) != before {
			t.Error("the kept sync plist changed")
		}
		log := e.calls(t)
		if !inOrder(log, "bootout "+e.target(daemonLabel), "print "+e.target(daemonLabel), "bootstrap "+e.gui()+" "+e.dp) ||
			strings.Contains(log, "bootout "+e.target(syncLabel)) {
			t.Errorf("launchctl calls:\n%s", log)
		}
	})
}

func TestServiceStatusRestartUninstallOnLaunchd(t *testing.T) {
	prev := bootoutWait
	bootoutWait = 300 * time.Millisecond
	t.Cleanup(func() { bootoutWait = prev })
	setup := func(t *testing.T, opt launchctlFake) (*launchdEnv, *httptest.Server) {
		e := newLaunchdEnv(t, opt)
		srv := e.listen(t)
		mustRun(t, "service", "install", "--satelled", "/x/satelled")
		e.clear(t)
		return e, srv
	}
	t.Run("status", func(t *testing.T) {
		e, srv := setup(t, launchctlFake{})
		out := mustRun(t, "service", "status")
		for _, want := range []string{
			"agent     " + e.dp + " (running)\n",
			"sync      " + e.sp + " (running)\n",
			fmt.Sprintf("satelled  answering at http://127.0.0.1:%d/healthz", e.port),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("status lacks %q:\n%s", want, out)
			}
		}
		srv.Close()
		out = mustRun(t, "service", "status")
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if !strings.HasPrefix(lines[len(lines)-1], "satelled  not answering at") {
			t.Errorf("status with the listener closed:\n%s", out)
		}
	})
	t.Run("restart", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		out := mustRun(t, "service", "restart")
		if !strings.Contains(e.calls(t), "kickstart -k "+e.target(daemonLabel)) || !strings.Contains(out, "dev.satelle.satelled restarted\n") {
			t.Errorf("restart: %s\n%s", out, e.calls(t))
		}
	})
	t.Run("restart fails", func(t *testing.T) {
		setup(t, launchctlFake{FailOn: "kickstart"})
		out, err := run(t, "", "service", "restart")
		if err == nil || !strings.Contains(err.Error(), "launchctl kickstart -k") {
			t.Errorf("restart: %v\n%s", err, out)
		}
	})
	removedBoth := func(t *testing.T, e *launchdEnv, out string) {
		t.Helper()
		if !strings.Contains(out, "removed "+e.sp+"\nremoved "+e.dp+"\n") {
			t.Errorf("uninstall output:\n%s", out)
		}
		if exists(e.dp) || exists(e.sp) {
			t.Error("a plist is left")
		}
	}
	keptBoth := func(t *testing.T, e *launchdEnv, out string) {
		t.Helper()
		if !exists(e.dp) || !exists(e.sp) {
			t.Error("a plist was removed")
		}
		if strings.Contains(out, "removed") {
			t.Errorf("uninstall output:\n%s", out)
		}
	}
	t.Run("uninstall", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		out := mustRun(t, "service", "uninstall")
		s, d := e.target(syncLabel), e.target(daemonLabel)
		if log := e.calls(t); !inOrder(log, "print "+s+"\n", "bootout "+s+"\n", "print "+s+"\n", "print "+d+"\n", "bootout "+d+"\n", "print "+d+"\n") {
			t.Errorf("launchctl calls:\n%s", log)
		}
		removedBoth(t, e, out)
		e.clear(t)
		out = mustRun(t, "service", "status")
		if !strings.Contains(out, "agent     "+e.dp+" (not installed)\n") || !strings.Contains(out, "sync      "+e.sp+" (not installed)\n") {
			t.Errorf("status after uninstall:\n%s", out)
		}
		if strings.Contains(e.calls(t), "print") {
			t.Errorf("status ran print with no plist:\n%s", e.calls(t))
		}
	})
	t.Run("uninstall, bootout fails but the agent is gone", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{FailOn: "bootout"})
		out := mustRun(t, "service", "uninstall")
		for _, label := range []string{syncLabel, daemonLabel} {
			if !inOrder(e.calls(t), "bootout "+e.target(label)+"\n", "print "+e.target(label)+"\n") {
				t.Errorf("no print after the bootout of %s:\n%s", label, e.calls(t))
			}
		}
		removedBoth(t, e, out)
	})
	t.Run("uninstall, bootout fails and the agent stays loaded", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{FailOn: "bootout", Stuck: true})
		out, err := run(t, "", "service", "uninstall")
		if err == nil || !strings.Contains(err.Error(), "launchctl bootout "+e.target(syncLabel)+": still loaded after") || !strings.Contains(err.Error(), "Boot-out failed: 5") {
			t.Errorf("uninstall: %v\n%s", err, out)
		}
		keptBoth(t, e, out)
		if strings.Contains(e.calls(t), "bootout "+e.target(daemonLabel)) {
			t.Errorf("satelled was booted out after the sync agent failed:\n%s", e.calls(t))
		}
	})
	t.Run("uninstall, bootout succeeds and the agent stays loaded", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{Stuck: true})
		out, err := run(t, "", "service", "uninstall")
		if err == nil || !strings.Contains(err.Error(), "still loaded after") || strings.Contains(err.Error(), "bootout:") {
			t.Errorf("uninstall: %v\n%s", err, out)
		}
		keptBoth(t, e, out)
	})
	failPrint := func(t *testing.T, e *launchdEnv) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(launchctlState(e.log), "fail-print"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("uninstall, print fails", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		failPrint(t, e)
		out, err := run(t, "", "service", "uninstall")
		if err == nil || !strings.Contains(err.Error(), "launchctl print "+e.target(syncLabel)) || !strings.Contains(err.Error(), "Bad request.") {
			t.Errorf("uninstall: %v\n%s", err, out)
		}
		keptBoth(t, e, out)
		if strings.Contains(e.calls(t), "bootout") {
			t.Errorf("bootout ran though the load state could not be read:\n%s", e.calls(t))
		}
	})
	t.Run("status, print fails", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		failPrint(t, e)
		out := mustRun(t, "service", "status")
		if !strings.Contains(out, "agent     "+e.dp+" (unknown: launchctl print") || !strings.Contains(out, "sync      "+e.sp+" (unknown: launchctl print") {
			t.Errorf("status with print failing:\n%s", out)
		}
	})
	t.Run("install, print fails", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		failPrint(t, e)
		out, err := run(t, "", "service", "install", "--satelled", "/x/satelled")
		if err == nil || !strings.Contains(err.Error(), "launchctl print ") {
			t.Errorf("install: %v\n%s", err, out)
		}
		if strings.Contains(e.calls(t), "bootout") || strings.Contains(e.calls(t), "bootstrap") {
			t.Errorf("install changed launchd though the load state could not be read:\n%s", e.calls(t))
		}
	})
	t.Run("uninstall, agents not loaded", func(t *testing.T) {
		e, _ := setup(t, launchctlFake{})
		loaded, _ := filepath.Glob(filepath.Join(launchctlState(e.log), "*.loaded"))
		for _, p := range loaded {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
		out := mustRun(t, "service", "uninstall")
		if strings.Contains(e.calls(t), "bootout") {
			t.Errorf("bootout of an agent not loaded:\n%s", e.calls(t))
		}
		removedBoth(t, e, out)
	})
}

func launchdDoctor(t *testing.T, args ...string) []byte {
	t.Helper()
	out, _ := run(t, "", append([]string{"doctor", "--json"}, args...)...)
	return []byte(out)
}

func TestDoctorReportsAndResolvesTheMissingLaunchAgent(t *testing.T) {
	t.Run("missing agent", func(t *testing.T) {
		e := newLaunchdEnv(t, launchctlFake{})
		e.listen(t)
		e.fakeDaemon(t)
		var rep doctorReport
		if err := json.Unmarshal(launchdDoctor(t), &rep); err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(rep.Issues, func(is doctorIssue) bool {
			return is.Check == "versions" && strings.Contains(is.Problem, "launch agent")
		})
		if i < 0 {
			t.Fatalf("no launch agent issue in %+v", rep.Issues)
		}
		is := rep.Issues[i]
		if !strings.Contains(is.Problem, "dev.satelle.satelled") || strings.Contains(is.Problem, "satelle.service") ||
			is.Fix != "satelle service install" || is.Resolve != resolveInstallService {
			t.Errorf("issue %+v", is)
		}
		var res resolveReport
		if err := json.Unmarshal(launchdDoctor(t, "--resolve"), &res); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(res.Actions, func(a resolveAction) bool { return a.Problem == is.Problem && a.Result == "applied" }) {
			t.Errorf("actions %+v", res.Actions)
		}
		if !exists(e.dp) || !exists(e.sp) {
			t.Error("--resolve did not write both plists")
		}
		rep = doctorReport{}
		if err := json.Unmarshal(launchdDoctor(t), &rep); err != nil {
			t.Fatal(err)
		}
		for _, is := range rep.Issues {
			if strings.Contains(is.Problem, "launch agent") {
				t.Errorf("issue left after --resolve: %+v", is)
			}
		}
	})
	t.Run("no satelled anywhere", func(t *testing.T) {
		newLaunchdEnv(t, launchctlFake{})
		var keep []string
		for _, d := range filepath.SplitList(os.Getenv("PATH")) {
			if !exists(filepath.Join(d, "satelled")) {
				keep = append(keep, d)
			}
		}
		t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
		var rep doctorReport
		if err := json.Unmarshal(launchdDoctor(t), &rep); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(rep.Notes, "versions: no satelled launch agent or binary is installed on this machine (satelle service install)") {
			t.Errorf("notes %q", rep.Notes)
		}
	})
}

func (e *launchdEnv) holdAgent(t *testing.T, label string, pid int, bin string) {
	t.Helper()
	state := launchctlState(e.log)
	writeFile(t, filepath.Join(state, label+".pid"), strconv.Itoa(pid)+"\n")
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	writeFile(t, filepath.Join(state, strconv.Itoa(pid)+".lsof"), fmt.Sprintf("p%d\nftxt\nD0x%x\ni%d\n", pid, uint32(sys.Dev), sys.Ino))
}

func fakeLsof(t *testing.T, state string) {
	t.Helper()
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "lsof"), fmt.Sprintf("#!/bin/sh\ncat %q/\"$3\".lsof 2>/dev/null || exit 1\n", state))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestServiceRefreshLeavesCurrentAgentsRunning(t *testing.T) {
	e := newLaunchdEnv(t, launchctlFake{})
	e.setPort(t, 1)
	sat := e.fakeDaemon(t)
	mustRun(t, "service", "refresh")
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeLsof(t, launchctlState(e.log))
	e.holdAgent(t, daemonLabel, 4242, sat)
	e.holdAgent(t, followLabel, 4343, cli)
	e.clear(t)
	out := mustRun(t, "service", "refresh")
	if log := e.calls(t); strings.Contains(log, "kickstart") || strings.Contains(log, "bootstrap") || strings.Contains(log, "bootout") {
		t.Errorf("a current agent was restarted:\n%s", log)
	}
	for _, want := range []string{
		daemonLabel + " loaded and already running " + sat + ", not restarted\n",
		followLabel + " loaded and already running " + cli + ", not restarted: writes push",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	other := filepath.Join(t.TempDir(), "satelled")
	writeExec(t, other, "#!/bin/sh\nexit 0\n")
	e.holdAgent(t, daemonLabel, 4242, other)
	e.clear(t)
	mustRun(t, "service", "refresh")
	if log := e.calls(t); !strings.Contains(log, "kickstart -k "+e.target(daemonLabel)) || strings.Contains(log, "kickstart -k "+e.target(followLabel)) {
		t.Errorf("only satelled, on another file, should restart:\n%s", log)
	}
}
