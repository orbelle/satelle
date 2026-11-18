//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func swapLaunchctl(t *testing.T) *fakeLaunchctl {
	t.Helper()
	f := &fakeLaunchctl{fail: notLoaded}
	prev := launchctl
	launchctl = f.run
	t.Cleanup(func() { launchctl = prev })
	return f
}

func TestDarwinServiceInstallUninstall(t *testing.T) {
	isolate(t)
	f := swapLaunchctl(t)
	must(t, "service", "install", "--satelled", "/x/satelled", "--port", "8799")
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	d := filepath.Join(dir, "dev.satelle.satelled.plist")
	s := filepath.Join(dir, "dev.satelle.sync.plist")
	for path, want := range map[string]string{
		d: renderDaemonPlist("/x/satelled", gc.Service.ResolveAddr(), 8799),
		s: renderSyncPlist(resolveCLI()),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Fatalf("%s:\n%s\nwant:\n%s", path, b, want)
		}
	}
	calls := fmt.Sprint(f.calls)
	for _, want := range []string{
		fmt.Sprintf("[bootstrap gui/%d %s]", os.Getuid(), d),
		fmt.Sprintf("[bootstrap gui/%d %s]", os.Getuid(), s),
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls %s lack %s", calls, want)
		}
	}
	f.calls = nil
	must(t, "service", "uninstall")
	for _, p := range []string{d, s} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still present: %v", p, err)
		}
	}
	if len(f.calls) != 2 || f.calls[0][0] != "bootout" || f.calls[1][0] != "bootout" {
		t.Fatalf("uninstall calls: %v", f.calls)
	}
}

func TestDarwinDoctorNamesLaunchAgent(t *testing.T) {
	isolate(t)
	f := swapLaunchctl(t)
	f.fail = func([]string) (string, error) { return "", errors.New("not loaded") }
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "satelled"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	issues, _ := checkVersions(context.Background(), nil)
	var found bool
	for _, is := range issues {
		if strings.Contains(is.Problem, "is not installed") {
			found = true
			if !strings.Contains(is.Problem, "satelled launch agent") || !strings.Contains(is.Problem, "Library/LaunchAgents/dev.satelle.satelled.plist") {
				t.Fatalf("problem: %s", is.Problem)
			}
			if strings.Contains(is.Problem, "satelle.service") {
				t.Fatalf("problem names the systemd unit: %s", is.Problem)
			}
			if is.Fix != "satelle service install" {
				t.Fatalf("fix: %s", is.Fix)
			}
		}
	}
	if !found {
		t.Fatalf("no missing-agent issue in %+v", issues)
	}
	ti, notes := timerIssues()
	if len(ti) != 1 || !strings.Contains(ti[0].Problem, "the sync launch agent") || ti[0].Fix != "satelle service install" {
		t.Fatalf("timer issues %+v", ti)
	}
	for _, n := range notes {
		if strings.Contains(n, "systemd-only") {
			t.Fatalf("note: %s", n)
		}
	}
}

func TestDarwinRefreshAndRestartSayNotSupported(t *testing.T) {
	isolate(t)
	swapLaunchctl(t)
	out := must(t, "service", "refresh")
	if !strings.Contains(out, "not supported yet") {
		t.Fatalf("refresh output: %s", out)
	}
	if _, err := run(t, "", "service", "restart"); err == nil || strings.Contains(err.Error(), "systemd") {
		t.Fatalf("restart err = %v", err)
	}
}
