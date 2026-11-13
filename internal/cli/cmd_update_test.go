package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// releaseServer serves a GitHub-shaped release API and download tree:
// assets maps "<tag>/<asset>" to its body; a ".sha256" beside each is
// derived unless badSum names an asset whose checksum is wrong.
func releaseServer(t *testing.T, latest string, list []string, assets map[string]string, badSum string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, latest)
	})
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		var parts []string
		for _, tag := range list {
			parts = append(parts, fmt.Sprintf(`{"tag_name":%q}`, tag))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/dl/")
		if base, ok := strings.CutSuffix(key, ".sha256"); ok {
			body, found := assets[base]
			if !found {
				http.NotFound(w, r)
				return
			}
			sum := sha256.Sum256([]byte(body))
			if base == badSum {
				sum = sha256.Sum256([]byte("tampered"))
			}
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(base))
			return
		}
		body, found := assets[key]
		if !found {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envReleaseAPI, srv.URL+"/latest")
	t.Setenv(envReleaseListAPI, srv.URL+"/list")
	t.Setenv(envReleaseBase, srv.URL+"/dl")
}

// fakeInstall points update at a satelle (and optionally a satelled) in a
// temp dir and returns their paths.
func fakeInstall(t *testing.T, withDaemon bool) (cli, daemon string) {
	t.Helper()
	dir := t.TempDir()
	cli = filepath.Join(dir, "satelle"+buildinfo.ExeSuffix())
	daemon = filepath.Join(dir, "satelled"+buildinfo.ExeSuffix())
	if err := os.WriteFile(cli, []byte("old cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withDaemon {
		if err := os.WriteFile(daemon, []byte("#!/bin/sh\necho 'satelled 0.1.0 (commit x, built y)'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev, prevRefresh := updateTarget, updateRefresh
	updateTarget = func() (string, error) { return cli, nil }
	updateRefresh = func(context.Context, string, io.Writer, io.Writer) error { return nil }
	t.Cleanup(func() { updateTarget, updateRefresh = prev, prevRefresh })
	return cli, daemon
}

func asset(name, version string) string {
	return fmt.Sprintf("%s-v%s-%s-%s%s", name, version, runtime.GOOS, runtime.GOARCH, buildinfo.ExeSuffix())
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateReplacesTheBinaryAndSatelledBeside(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v0.2.0", "serve-v0.1.0"}, map[string]string{
		"v2.0.0/" + asset("satelle", "2.0.0"):        "new cli",
		"serve-v0.2.0/" + asset("satelled", "0.2.0"): "new daemon",
	}, "")
	out := must(t, "update")
	if !strings.Contains(out, "satelle "+buildinfo.Version+" → 2.0.0") || !strings.Contains(out, "satelled 0.1.0 → 0.2.0") ||
		!strings.Contains(out, "satelle service restart") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "new cli" {
		t.Errorf("cli = %q", got)
	}
	if got := readString(t, daemon); got != "new daemon" {
		t.Errorf("satelled = %q", got)
	}
	if fi, err := os.Stat(cli); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the new binary is not executable: %v %v", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(cli), ".*new*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestUpdateToAVersionWithoutSatelled(t *testing.T) {
	isolate(t)
	cli, daemon := fakeInstall(t, false)
	releaseServer(t, "v2.0.0", nil, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"): "pinned cli",
	}, "")
	out := must(t, "update", "--version", "1.5.0")
	if !strings.Contains(out, "→ 1.5.0") || strings.Contains(out, "\nsatelled ") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "pinned cli" {
		t.Errorf("cli = %q", got)
	}
	if _, err := os.Stat(daemon); err == nil {
		t.Error("update created a satelled that was not there")
	}
}

func TestUpdateRefusesABadChecksum(t *testing.T) {
	isolate(t)
	cli, _ := fakeInstall(t, false)
	a := "v2.0.0/" + asset("satelle", "2.0.0")
	releaseServer(t, "v2.0.0", nil, map[string]string{a: "evil cli"}, a)
	if _, err := run(t, "", "update"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("update with a bad checksum: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the binary was replaced: %q", got)
	}
}

func TestUpdateMissingAssetFails(t *testing.T) {
	isolate(t)
	cli, _ := fakeInstall(t, false)
	releaseServer(t, "v3.0.0", nil, map[string]string{}, "")
	if _, err := run(t, "", "update"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("update with no asset: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the binary was replaced: %q", got)
	}
}

func TestNewestServeTagComparesNumerically(t *testing.T) {
	for _, c := range []struct {
		tags []string
		want string
		ok   bool
	}{
		{[]string{"v1.0.10", "serve-v1.0.9", "v1.0.9", "serve-v1.0.10"}, "serve-v1.0.10", true},
		{[]string{"serve-v0.0.297", "serve-v1.0.2", "serve-v1.0.10", "serve-v1.0.9"}, "serve-v1.0.10", true},
		{[]string{"serve-vnext", "serve-v1.0.1"}, "serve-v1.0.1", true},
		{[]string{"serve-v1.0", "serve-v1.0.1"}, "serve-v1.0.1", true},
		{[]string{"v2.0.0", "v1.0.0"}, "", false},
	} {
		got, ok := newestServeTag(c.tags)
		if got != c.want || ok != c.ok {
			t.Errorf("newestServeTag(%v) = %q %v, want %q %v", c.tags, got, ok, c.want, c.ok)
		}
	}
}

// GitHub lists serve-v1.0.9 before serve-v1.0.10; update takes 1.0.10.
func TestUpdateInstallsTheNewestSatelledNotTheFirstListed(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	_, daemon := fakeInstall(t, true)
	releaseServer(t, "v1.0.10", []string{"v1.0.10", "serve-v1.0.9", "v1.0.9", "serve-v1.0.10"}, map[string]string{
		"v1.0.10/" + asset("satelle", "1.0.10"):        "cli 1.0.10",
		"serve-v1.0.9/" + asset("satelled", "1.0.9"):   "daemon 1.0.9",
		"serve-v1.0.10/" + asset("satelled", "1.0.10"): "daemon 1.0.10",
	}, "")
	out := must(t, "update")
	if !strings.Contains(out, "satelled 0.1.0 → 1.0.10") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, daemon); got != "daemon 1.0.10" {
		t.Errorf("satelled = %q", got)
	}
}

func TestUpdatePinsSatelledToTheVersion(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v2.0.0"}, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"):        "pinned cli",
		"serve-v1.5.0/" + asset("satelled", "1.5.0"): "pinned daemon",
		"serve-v2.0.0/" + asset("satelled", "2.0.0"): "newer daemon",
	}, "")
	out := must(t, "update", "--version", "1.5.0")
	if !strings.Contains(out, "satelled 0.1.0 → 1.5.0") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "pinned cli" {
		t.Errorf("cli = %q", got)
	}
	if got := readString(t, daemon); got != "pinned daemon" {
		t.Errorf("satelled = %q", got)
	}
}

func TestUpdatePinnedSatelledWithoutAssetRefuses(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	before := readString(t, daemon)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v2.0.0"}, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"):        "pinned cli",
		"serve-v2.0.0/" + asset("satelled", "2.0.0"): "newer daemon",
	}, "")
	_, err := run(t, "", "update", "--version", "1.5.0")
	if err == nil || !strings.Contains(err.Error(), "serve-v1.5.0") || !strings.Contains(err.Error(), asset("satelled", "1.5.0")) {
		t.Fatalf("pinned update without a satelled asset: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the cli was replaced: %q", got)
	}
	if got := readString(t, daemon); got != before {
		t.Errorf("satelled was replaced: %q", got)
	}
}

// update records the builds it installs, so satelled's footer names the new CLI
// and satelled straight after the update without running either (sto_49b6e762).
func TestUpdateRecordsTheInstalledBuilds(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake binaries are shell scripts")
	}
	cli, daemon := fakeInstall(t, true)
	cliLine := "satelle 2.0.0 (commit c, built b)"
	daemonLine := "satelled 0.2.0 (commit d, built b)"
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v0.2.0", "serve-v0.1.0"}, map[string]string{
		"v2.0.0/" + asset("satelle", "2.0.0"):        "#!/bin/sh\necho '" + cliLine + "'\n",
		"serve-v0.2.0/" + asset("satelled", "0.2.0"): "#!/bin/sh\necho '" + daemonLine + "'\n",
	}, "")
	must(t, "update")
	home := config.GlobalDir()
	if got := buildinfo.Lookup(home, cli); got != cliLine {
		t.Errorf("recorded CLI = %q, want %q", got, cliLine)
	}
	if got := buildinfo.Lookup(home, daemon); got != daemonLine {
		t.Errorf("recorded satelled = %q, want %q", got, daemonLine)
	}
	if got := buildinfo.Lookup(home, buildinfo.CLIBeside(daemon)); got != cliLine {
		t.Errorf("the CLI beside satelled = %q, want %q", got, cliLine)
	}
}

// fakeSystemctl puts a systemctl first on PATH that appends each call's
// arguments to the returned log and exits 1 on any call containing failOn
// (never, when ""), and points [service] at a port nothing answers on, so no
// test reaches this machine's user manager or satelled.
func fakeSystemctl(t *testing.T, failOn string) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "systemctl.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	if failOn != "" {
		script += "case \"$*\" in *" + failOn + "*) exit 1;; esac\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.MkdirAll(config.GlobalDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.GlobalDir(), config.GlobalConfigName), []byte("[service]\nport = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return log
}

func userUnitDir() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
}

// inOrder reports whether each of want occurs in s, each after the last.
func inOrder(s string, want ...string) bool {
	for _, w := range want {
		i := strings.Index(s, w)
		if i < 0 {
			return false
		}
		s = s[i+len(w):]
	}
	return true
}

// satelle update installs and refreshes the service: a missing unit is
// written, one naming another binary is repointed, satelled is restarted and
// the sync timer started, then the status is printed; with no systemd user
// manager the units are printed and update still succeeds (sto_1b6a7ed3 AC1).
func TestUpdateInstallsTheServiceAndTimer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the units are systemd-only")
	}
	cases := []struct {
		name     string
		unit     func(daemon string) string // satelled's unit before the update; "" = none
		failOn   string
		check    func(t *testing.T, out, calls, daemon, cli string)
		noUnits  bool
		wantKept bool
	}{
		{name: "unit present, no timer", unit: func(d string) string { return renderUnit(d, "127.0.0.1", 1) }, wantKept: true},
		{name: "unit names another binary", unit: func(string) string { return renderUnit("/old/satelled", "127.0.0.1", 1) },
			check: func(t *testing.T, out, _, daemon, _ string) {
				if !strings.Contains(out, "(was /old/satelled)") {
					t.Errorf("the repoint is not reported:\n%s", out)
				}
				if got := unitDaemon(readString(t, filepath.Join(userUnitDir(), serviceUnitName))); got != daemon {
					t.Errorf("unit runs %q, want %q", got, daemon)
				}
			}},
		{name: "no units at all", unit: func(string) string { return "" },
			check: func(t *testing.T, out, _, daemon, _ string) {
				if got := unitDaemon(readString(t, filepath.Join(userUnitDir(), serviceUnitName))); got != daemon {
					t.Errorf("unit runs %q, want %q", got, daemon)
				}
				if strings.Count(out, "(missing)") != 3 {
					t.Errorf("want three units written as missing:\n%s", out)
				}
			}},
		{name: "no systemd user manager", unit: func(string) string { return "" }, failOn: "show-environment", noUnits: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			t.Setenv(envNoService, "")
			log := fakeSystemctl(t, tc.failOn)
			cli, daemon := fakeInstall(t, true)
			releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
			unitFile := filepath.Join(userUnitDir(), serviceUnitName)
			if body := tc.unit(daemon); body != "" {
				if err := os.MkdirAll(userUnitDir(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unitFile, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(unitFile)
			out, err := run(t, "", "update")
			if err != nil {
				t.Fatalf("update: %v\n%s", err, out)
			}
			calls, _ := os.ReadFile(log)
			if tc.noUnits {
				if !strings.Contains(out, "[Unit]") || !strings.Contains(out, "satelle ledger autosync") {
					t.Errorf("the units and the autosync command are not printed:\n%s", out)
				}
				if _, err := os.Stat(userUnitDir()); !os.IsNotExist(err) {
					t.Errorf("a unit was written without systemd: %v", err)
				}
				if strings.Contains(string(calls), "restart") {
					t.Errorf("systemctl restarted without a user manager:\n%s", calls)
				}
				return
			}
			if got := readString(t, filepath.Join(userUnitDir(), syncTimerName)); got != renderSyncTimer() {
				t.Errorf("timer file:\n%s", got)
			}
			if !strings.Contains(readString(t, filepath.Join(userUnitDir(), syncServiceName)), "ExecStart="+cli+" ledger autosync\n") {
				t.Errorf("the sync service does not run %s ledger autosync", cli)
			}
			if tc.wantKept {
				if !strings.Contains(out, "kept "+unitFile) || readString(t, unitFile) != string(before) {
					t.Errorf("satelled's unit was not kept:\n%s", out)
				}
			}
			if !inOrder(string(calls), "--user daemon-reload\n", "--user enable "+serviceUnitName+"\n",
				"--user restart "+serviceUnitName+"\n", "--user enable --now "+syncTimerName+"\n") {
				t.Errorf("systemctl calls:\n%s", calls)
			}
			for _, want := range []string{"\nunit ", "\nsync ", "\nconfig "} {
				if !strings.Contains(out, want) {
					t.Errorf("the status line %q is not printed:\n%s", want, out)
				}
			}
			if tc.check != nil {
				tc.check(t, out, string(calls), daemon, cli)
			}
		})
	}
}

// --no-service and SATELLE_NO_SERVICE=1 change no unit and run no systemctl,
// and say what was skipped and how to restart (sto_1b6a7ed3 AC2).
func TestUpdateNoServiceSkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake systemctl and satelled are shell scripts")
	}
	for _, tc := range []struct{ name, env, why string }{
		{name: "flag", why: "--no-service"},
		{name: "env", env: "1", why: "SATELLE_NO_SERVICE=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			t.Setenv(envNoService, tc.env)
			log := fakeSystemctl(t, "")
			fakeInstall(t, true)
			releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
			args := []string{"update"}
			if tc.env == "" {
				args = append(args, "--no-service")
			}
			out, err := run(t, "", args...)
			if err != nil {
				t.Fatalf("update: %v\n%s", err, out)
			}
			if _, err := os.Stat(userUnitDir()); !os.IsNotExist(err) {
				t.Errorf("a unit dir was created: %v", err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Errorf("systemctl ran: %v", err)
			}
			if !strings.Contains(out, "("+tc.why+")") || !strings.Contains(out, "satelle service restart") {
				t.Errorf("the skip line does not name %s and the restart:\n%s", tc.why, out)
			}
		})
	}
}

// satelle help service says install and update install the service and sync,
// and names the opt-out and refresh (sto_1b6a7ed3 AC3).
func TestHelpServiceNamesInstallUpdateAndOptOut(t *testing.T) {
	isolate(t)
	out := must(t, "help", "service")
	for _, want := range []string{"satelle update", "install.sh", "--no-service", "SATELLE_NO_SERVICE=1", "refresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("help service lacks %q:\n%s", want, out)
		}
	}
}

// legacySetup is a v1 repo (the working directory), two registered pre-1.0
// projects and a registered path that is gone, as update reads them
// (sto_0f76dd4e).
func legacySetup(t *testing.T, dir string) (oldA, oldB, gone string) {
	t.Helper()
	must(t, "init")
	oldA, _ = newPreV1Repo(t)
	oldB, _ = newPreV1Repo(t)
	gone = filepath.Join(t.TempDir(), "gone")
	registerProjects(t, oldA, dir, gone, oldB)
	return oldA, oldB, gone
}

// checkUpgradeList asserts that out, after the line holding after, lists
// exactly olds, in order, as needing upgrade, and that nothing about them is
// printed before it.
func checkUpgradeList(t *testing.T, out, after, dir, gone string, olds ...string) {
	t.Helper()
	i := strings.Index(out, after)
	if i < 0 {
		t.Fatalf("no %q line:\n%s", after, out)
	}
	if strings.Contains(out[:i], "needs upgrade") || strings.Contains(out[:i], "upgrade --archive") {
		t.Errorf("a pre-1.0 project was printed before the replacement:\n%s", out)
	}
	rest := out[i:]
	head := fmt.Sprintf("%d registered project(s) need `satelle upgrade --archive`:\n", len(olds))
	if !strings.Contains(rest, head) {
		t.Fatalf("no %q after %q:\n%s", head, after, out)
	}
	var lines []string
	for _, l := range strings.Split(rest, "\n") {
		if strings.Contains(l, "needs upgrade") {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(olds) {
		t.Fatalf("needs-upgrade lines %q, want %d", lines, len(olds))
	}
	for i, p := range olds {
		if !strings.HasPrefix(lines[i], p+": needs upgrade") || !strings.Contains(lines[i], "satelle upgrade --archive") {
			t.Errorf("line %d = %q, want %s", i, lines[i], p)
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, dir+":") || strings.HasPrefix(l, gone+":") {
			t.Errorf("a v1 or missing project is listed: %q", l)
		}
	}
}

// AC4 (sto_0f76dd4e): after installing, update lists every registered pre-1.0
// project with the command to run in it, and prints nothing about them before
// the replacement; it does so when current too, and when the service refresh
// fails.
func TestUpdateListsProjectsThatNeedUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	t.Run("replaced", func(t *testing.T) {
		dir := isolate(t)
		oldA, oldB, gone := legacySetup(t, dir)
		cli, _ := fakeInstall(t, false)
		releaseServer(t, "v2.0.0", nil, map[string]string{"v2.0.0/" + asset("satelle", "2.0.0"): "new cli"}, "")
		out := must(t, "update")
		if got := readString(t, cli); got != "new cli" {
			t.Errorf("cli = %q", got)
		}
		checkUpgradeList(t, out, "satelle "+buildinfo.Version+" → 2.0.0", dir, gone, oldA, oldB)
	})
	t.Run("current", func(t *testing.T) {
		dir := isolate(t)
		oldA, oldB, gone := legacySetup(t, dir)
		fakeInstall(t, false)
		releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
		out := must(t, "update")
		checkUpgradeList(t, out, "satelle "+buildinfo.Version+" is current", dir, gone, oldA, oldB)
	})
	t.Run("service refresh fails", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the units are systemd-only")
		}
		dir := isolate(t)
		t.Setenv(envNoService, "")
		fakeSystemctl(t, "daemon-reload")
		oldA, oldB, gone := legacySetup(t, dir)
		fakeInstall(t, true)
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		out, err := run(t, "", "update")
		if err == nil || !strings.Contains(err.Error(), "the service is not") {
			t.Fatalf("want the service error, got %v\n%s", err, out)
		}
		checkUpgradeList(t, out, "satelle "+buildinfo.Version+" is current", dir, gone, oldA, oldB)
	})
}

// AC4 (sto_0f76dd4e): with no pre-1.0 project registered, update says
// nothing about upgrading.
func TestUpdateQuietWithoutPreV1Projects(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	registerProjects(t, dir)
	fakeInstall(t, false)
	releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
	out := must(t, "update")
	if strings.Contains(out, "needs upgrade") || strings.Contains(out, "satelle upgrade") {
		t.Errorf("update named an upgrade with none registered:\n%s", out)
	}
}
