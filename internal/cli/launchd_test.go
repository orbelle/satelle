package cli

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

const goldenDaemonPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.satelle.satelled</string>
	<key>ProgramArguments</key>
	<array>
		<string>/x/satelled</string>
		<string>--addr</string>
		<string>127.0.0.1</string>
		<string>--port</string>
		<string>8787</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`

func goldenSyncPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.satelle.sync</string>
	<key>ProgramArguments</key>
	<array>
		<string>/x/satelle</string>
		<string>ledger</string>
		<string>autosync</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>` + fmt.Sprint(int(mirror.SyncPeriod.Seconds())) + `</integer>
</dict>
</plist>
`
}

func TestRenderDaemonPlist(t *testing.T) {
	got := renderDaemonPlist("/x/satelled", "127.0.0.1", 8787)
	if got != goldenDaemonPlist {
		t.Fatalf("daemon plist:\n%s\nwant:\n%s", got, goldenDaemonPlist)
	}
	var v any
	if err := xml.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("daemon plist is not XML: %v", err)
	}
	if p := plistProgram(got); p != "/x/satelled" {
		t.Fatalf("plistProgram = %q, want /x/satelled", p)
	}
}

func TestRenderSyncPlist(t *testing.T) {
	got := renderSyncPlist("/x/satelle")
	if got != goldenSyncPlist() {
		t.Fatalf("sync plist:\n%s\nwant:\n%s", got, goldenSyncPlist())
	}
	if fmt.Sprint(int(mirror.SyncPeriod.Seconds())) != "30" {
		t.Fatalf("sync period is %s, the card says 30 s", mirror.SyncPeriod)
	}
	if p := plistProgram(got); p != "/x/satelle" {
		t.Fatalf("plistProgram = %q, want /x/satelle", p)
	}
}

func TestPlistEscapes(t *testing.T) {
	got := renderDaemonPlist("/a&b/<c>/satelled", "127.0.0.1", 1)
	if !strings.Contains(got, "<string>/a&amp;b/&lt;c&gt;/satelled</string>") {
		t.Fatalf("path not escaped:\n%s", got)
	}
	if p := plistProgram(got); p != "/a&b/<c>/satelled" {
		t.Fatalf("plistProgram = %q", p)
	}
}

func TestPlistProgramUnreadable(t *testing.T) {
	for _, body := range []string{"", "not xml", "<plist><dict><key>Label</key><string>x</string></dict></plist>"} {
		if p := plistProgram(body); p != "" {
			t.Fatalf("plistProgram(%q) = %q, want empty", body, p)
		}
	}
}

func TestBootoutNotLoaded(t *testing.T) {
	for out, want := range map[string]bool{
		"Boot-out failed: 3: No such process":         true,
		"Could not find specified service":            true,
		"Service is not loaded":                       true,
		"Boot-out failed: 1: Operation not permitted": false,
		"": false,
	} {
		if got := bootoutNotLoaded(out); got != want {
			t.Errorf("bootoutNotLoaded(%q) = %v, want %v", out, got, want)
		}
	}
}

type fakeLaunchctl struct {
	calls [][]string
	fail  func(args []string) (string, error)
}

func (f *fakeLaunchctl) run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.fail != nil {
		return f.fail(args)
	}
	return "", nil
}

func notLoaded(args []string) (string, error) {
	if args[0] == "bootout" {
		return "Boot-out failed: 3: No such process", errors.New("exit status 3")
	}
	return "", nil
}

func TestInstallLaunchAgents(t *testing.T) {
	spec := launchSpec{Daemon: "/x/satelled", Addr: "127.0.0.1", Port: 8787, CLI: "/x/satelle"}
	t.Run("first install", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeLaunchctl{fail: notLoaded}
		var out bytes.Buffer
		if err := installLaunchAgents(&out, f.run, dir, 501, spec); err != nil {
			t.Fatal(err)
		}
		d := launchPlistPath(dir, launchDaemonLabel)
		s := launchPlistPath(dir, launchSyncLabel)
		for path, want := range map[string]string{d: goldenDaemonPlist, s: goldenSyncPlist()} {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != want {
				t.Fatalf("%s:\n%s\nwant:\n%s", path, b, want)
			}
		}
		want := [][]string{
			{"bootout", "gui/501/dev.satelle.satelled"},
			{"enable", "gui/501/dev.satelle.satelled"},
			{"bootstrap", "gui/501", d},
			{"bootout", "gui/501/dev.satelle.sync"},
			{"enable", "gui/501/dev.satelle.sync"},
			{"bootstrap", "gui/501", s},
		}
		if !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("launchctl calls:\n%v\nwant:\n%v", f.calls, want)
		}
		if !strings.Contains(out.String(), "dev.satelle.sync loaded in gui/501") {
			t.Fatalf("output: %s", out.String())
		}
	})
	t.Run("bootstrap fails", func(t *testing.T) {
		f := &fakeLaunchctl{fail: func(args []string) (string, error) {
			if args[0] == "bootstrap" {
				return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
			}
			return notLoaded(args)
		}}
		err := installLaunchAgents(&bytes.Buffer{}, f.run, t.TempDir(), 501, spec)
		if err == nil || !strings.Contains(err.Error(), "launchctl bootstrap gui/501 ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bootout fails", func(t *testing.T) {
		f := &fakeLaunchctl{fail: func(args []string) (string, error) {
			if args[0] == "bootout" {
				return "Boot-out failed: 1: Operation not permitted", errors.New("exit status 1")
			}
			return "", nil
		}}
		err := installLaunchAgents(&bytes.Buffer{}, f.run, t.TempDir(), 501, spec)
		if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
			t.Fatalf("err = %v", err)
		}
		for _, c := range f.calls {
			if c[0] == "bootstrap" {
				t.Fatalf("bootstrap ran after a failed bootout: %v", f.calls)
			}
		}
	})
	t.Run("no daemon", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeLaunchctl{fail: notLoaded}
		if err := installLaunchAgents(&bytes.Buffer{}, f.run, dir, 501, launchSpec{CLI: "/x/satelle"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(launchPlistPath(dir, launchDaemonLabel)); !os.IsNotExist(err) {
			t.Fatalf("daemon plist written without a daemon: %v", err)
		}
		if len(f.calls) != 3 {
			t.Fatalf("calls: %v", f.calls)
		}
	})
}

func TestUninstallLaunchAgents(t *testing.T) {
	spec := launchSpec{Daemon: "/x/satelled", Addr: "127.0.0.1", Port: 8787, CLI: "/x/satelle"}
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		if err := installLaunchAgents(&bytes.Buffer{}, (&fakeLaunchctl{fail: notLoaded}).run, dir, 501, spec); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	wantCalls := [][]string{{"bootout", "gui/501/dev.satelle.sync"}, {"bootout", "gui/501/dev.satelle.satelled"}}
	gone := func(t *testing.T, dir string, want bool) {
		t.Helper()
		for _, l := range []string{launchDaemonLabel, launchSyncLabel} {
			_, err := os.Stat(filepath.Join(dir, l+".plist"))
			if os.IsNotExist(err) != want {
				t.Fatalf("%s: removed=%v, want %v", l, os.IsNotExist(err), want)
			}
		}
	}
	t.Run("loaded", func(t *testing.T) {
		dir := setup(t)
		f := &fakeLaunchctl{}
		if err := uninstallLaunchAgents(&bytes.Buffer{}, f.run, dir, 501); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.calls, wantCalls) {
			t.Fatalf("calls %v, want %v", f.calls, wantCalls)
		}
		gone(t, dir, true)
	})
	t.Run("not loaded", func(t *testing.T) {
		dir := setup(t)
		f := &fakeLaunchctl{fail: notLoaded}
		if err := uninstallLaunchAgents(&bytes.Buffer{}, f.run, dir, 501); err != nil {
			t.Fatal(err)
		}
		gone(t, dir, true)
	})
	t.Run("real failure", func(t *testing.T) {
		dir := setup(t)
		f := &fakeLaunchctl{fail: func(args []string) (string, error) {
			if args[1] == "gui/501/dev.satelle.sync" {
				return "Boot-out failed: 1: Operation not permitted", errors.New("exit status 1")
			}
			return "", nil
		}}
		err := uninstallLaunchAgents(&bytes.Buffer{}, f.run, dir, 501)
		if err == nil || !strings.Contains(err.Error(), "launchctl bootout gui/501/dev.satelle.sync") {
			t.Fatalf("err = %v", err)
		}
		if !reflect.DeepEqual(f.calls, wantCalls) {
			t.Fatalf("calls %v, want %v", f.calls, wantCalls)
		}
		gone(t, dir, false)
	})
}
