//go:build linux

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func systemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// installUnit writes satelled's unit and the sync service and timer, then
// enables and (re)starts satelled and enables and starts the timer.
func installUnit(w io.Writer, u serviceUnits) error {
	for _, f := range []struct{ name, body string }{
		{serviceUnitName, u.Daemon}, {syncServiceName, u.SyncService}, {syncTimerName, u.SyncTimer},
	} {
		path := userUnitPath(f.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s\n", path)
	}
	for _, args := range [][]string{
		{"daemon-reload"}, {"enable", serviceUnitName}, {"restart", serviceUnitName}, {"enable", "--now", syncTimerName},
	} {
		if out, err := systemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Fprintf(w, "%s enabled and started\n", serviceUnitName)
	fmt.Fprintf(w, "%s enabled and started: each project on this machine syncs every %s\n", syncTimerName, syncPeriod)
	return nil
}

func uninstallUnit(w io.Writer) error {
	_, _ = systemctl("disable", "--now", syncTimerName)
	_, _ = systemctl("disable", "--now", serviceUnitName)
	for _, name := range []string{syncTimerName, syncServiceName, serviceUnitName} {
		path := userUnitPath(name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(w, "removed %s\n", path)
	}
	_, _ = systemctl("daemon-reload")
	return nil
}

func restartUnit(w io.Writer) error {
	if out, err := systemctl("restart", serviceUnitName); err != nil {
		return fmt.Errorf("systemctl --user restart %s: %v: %s", serviceUnitName, err, out)
	}
	fmt.Fprintf(w, "%s restarted\n", serviceUnitName)
	return nil
}

func unitState() string { return activeState(serviceUnitName) }

func syncTimerState() string { return activeState(syncTimerName) }

func activeState(name string) string {
	out, _ := systemctl("is-active", name)
	if out == "" {
		return "unknown"
	}
	return out
}
