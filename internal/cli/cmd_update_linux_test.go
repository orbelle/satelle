//go:build linux

package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

func copyTestBinary(t *testing.T, dst string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		t.Fatal(err)
	}
}

func startHold(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), envTestHold+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

type holdEnv struct {
	cli, daemon, log, out string
	daemonPID, followPID  int
}

func (h *holdEnv) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func setPID(t *testing.T, log, unit string, pid int) {
	t.Helper()
	dir := systemctlPIDs(log)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, unit+".pid"), strconv.Itoa(pid)+"\n")
}

func newHoldEnv(t *testing.T) *holdEnv {
	t.Helper()
	isolate(t)
	t.Setenv(envNoService, "")
	h := &holdEnv{}
	h.log = fakeSystemctl(t, "")
	h.cli, h.daemon = fakeInstall(t, true)
	copyTestBinary(t, h.cli)
	copyTestBinary(t, h.daemon)
	home := config.GlobalDir()
	if err := buildinfo.Record(home, h.cli, "satelle "+buildinfo.Version+" (commit x, built y)"); err != nil {
		t.Fatal(err)
	}
	if err := buildinfo.Record(home, h.daemon, "satelled 0.1.0 (commit x, built y)"); err != nil {
		t.Fatal(err)
	}
	h.daemonPID = startHold(t, h.daemon)
	h.followPID = startHold(t, h.cli)
	setPID(t, h.log, serviceUnitName, h.daemonPID)
	setPID(t, h.log, followServiceName, h.followPID)
	u := unitsFor(serviceSpec{Daemon: h.daemon, Addr: "0.0.0.0", Port: 1, CLI: h.cli})
	for name, body := range map[string]string{serviceUnitName: u.Daemon, syncServiceName: u.SyncService, syncTimerName: u.SyncTimer, followServiceName: u.Follow} {
		writeFile(t, filepath.Join(userUnitDir(), name), body)
	}
	return h
}

func (h *holdEnv) update(t *testing.T) {
	t.Helper()
	out, err := run(t, "", "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	h.out = out
}

func TestUpdateLeavesCurrentSatelledRunning(t *testing.T) {
	h := newHoldEnv(t)
	releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
	h.update(t)
	calls := h.calls(t)
	for _, unit := range []string{serviceUnitName, followServiceName} {
		if strings.Contains(calls, "--user restart "+unit+"\n") {
			t.Errorf("%s was restarted:\n%s", unit, calls)
		}
		if !strings.Contains(calls, "--user enable "+unit+"\n") {
			t.Errorf("%s was not enabled:\n%s", unit, calls)
		}
	}
	if !strings.Contains(calls, "--user enable --now "+syncTimerName+"\n") {
		t.Errorf("the sync timer was not enabled:\n%s", calls)
	}
	if strings.Count(h.out, "kept ") != 4 || strings.Contains(h.out, "wrote ") {
		t.Errorf("a unit was rewritten:\n%s", h.out)
	}
	for _, want := range []string{
		serviceUnitName + " enabled and already running " + h.daemon + ", not restarted\n",
		followServiceName + " enabled and already running " + h.cli + ", not restarted: writes push",
	} {
		if !strings.Contains(h.out, want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if !alive(h.daemonPID) || !alive(h.followPID) {
		t.Errorf("a held process died: satelled %v, follow %v", alive(h.daemonPID), alive(h.followPID))
	}
}

func TestUpdateRestartsWhatChanged(t *testing.T) {
	restarted := func(t *testing.T, h *holdEnv, unit string, want bool) {
		t.Helper()
		if got := strings.Contains(h.calls(t), "--user restart "+unit+"\n"); got != want {
			t.Errorf("%s restarted = %v, want %v:\n%s\n%s", unit, got, want, h.calls(t), h.out)
		}
	}
	t.Run("satelled replaced", func(t *testing.T) {
		h := newHoldEnv(t)
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.2.0", "serve-v0.1.0"}, map[string]string{
			"serve-v0.2.0/" + asset("satelled", "0.2.0"): "#!/bin/sh\necho 'satelled 0.2.0 (commit d, built b)'\n",
		}, "")
		h.update(t)
		if !inOrder(h.calls(t), "--user enable "+serviceUnitName+"\n", "--user restart "+serviceUnitName+"\n") {
			t.Errorf("satelled was not restarted after its enable:\n%s", h.calls(t))
		}
		if !strings.Contains(h.out, "satelled 0.1.0 → 0.2.0") || !strings.Contains(h.out, serviceUnitName+" enabled and (re)started\n") {
			t.Errorf("output:\n%s", h.out)
		}
		restarted(t, h, followServiceName, false)
	})
	t.Run("unit names another binary", func(t *testing.T) {
		h := newHoldEnv(t)
		writeFile(t, filepath.Join(userUnitDir(), serviceUnitName), renderUnit("/old/satelled", "0.0.0.0", 1))
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		h.update(t)
		if !strings.Contains(h.out, "(was /old/satelled)") {
			t.Errorf("output:\n%s", h.out)
		}
		restarted(t, h, serviceUnitName, true)
		if strings.Contains(h.calls(t), "show -p MainPID --value "+serviceUnitName) {
			t.Errorf("a written unit was probed:\n%s", h.calls(t))
		}
		restarted(t, h, followServiceName, false)
	})
	t.Run("unit missing", func(t *testing.T) {
		h := newHoldEnv(t)
		for _, name := range []string{serviceUnitName, followServiceName} {
			if err := os.Remove(filepath.Join(userUnitDir(), name)); err != nil {
				t.Fatal(err)
			}
		}
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		h.update(t)
		if strings.Count(h.out, "(missing)") != 2 {
			t.Errorf("output:\n%s", h.out)
		}
		restarted(t, h, serviceUnitName, true)
		restarted(t, h, followServiceName, true)
	})
	t.Run("unit bytes differ", func(t *testing.T) {
		h := newHoldEnv(t)
		unit := filepath.Join(userUnitDir(), serviceUnitName)
		writeFile(t, unit, renderUnit(h.daemon, "0.0.0.0", 2))
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		h.update(t)
		if !strings.Contains(h.out, "wrote "+unit+" (changed)\n") {
			t.Errorf("output:\n%s", h.out)
		}
		restarted(t, h, serviceUnitName, true)
		restarted(t, h, followServiceName, false)
	})
	t.Run("follow not on the installed satelle", func(t *testing.T) {
		h := newHoldEnv(t)
		other := filepath.Join(t.TempDir(), "satelle")
		copyTestBinary(t, other)
		setPID(t, h.log, followServiceName, startHold(t, other))
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		h.update(t)
		restarted(t, h, followServiceName, true)
		restarted(t, h, serviceUnitName, false)
	})
	t.Run("satelled not running", func(t *testing.T) {
		h := newHoldEnv(t)
		setPID(t, h.log, serviceUnitName, 0)
		releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
		h.update(t)
		restarted(t, h, serviceUnitName, true)
		restarted(t, h, followServiceName, false)
	})
}
