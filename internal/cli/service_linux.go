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

// systemdUser reports whether a systemd user manager is reachable: systemctl
// is on PATH and 'systemctl --user show-environment' succeeds.
func systemdUser() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := systemctl("show-environment")
	return err == nil
}

// applyUnits installs satelled's unit and the sync service and timer, each
// whose body is not empty: with force every one is written; without it, one
// is written only when it is missing or its ExecStart names another binary,
// and the rest are kept. It then reloads, enables and restarts satelled when
// its unit exists, and enables and starts the timer. Without a systemd user
// manager it prints the units and returns errNoSystemd.
func applyUnits(w io.Writer, u serviceUnits, force bool) error {
	if !systemdUser() {
		printUnits(w, u)
		return errNoSystemd
	}
	for _, f := range []struct{ name, body string }{
		{serviceUnitName, u.Daemon}, {syncServiceName, u.SyncService}, {syncTimerName, u.SyncTimer},
	} {
		if f.body == "" {
			continue
		}
		path := userUnitPath(f.name)
		why := ""
		if !force {
			old, err := os.ReadFile(path)
			switch {
			case err != nil:
				why = " (missing)"
			case !sameBinary(unitDaemon(string(old)), unitDaemon(f.body)):
				why = " (was " + unitDaemon(string(old)) + ")"
			default:
				fmt.Fprintf(w, "kept %s\n", path)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s%s\n", path, why)
	}
	steps := [][]string{{"daemon-reload"}}
	_, daemonErr := os.Stat(unitPath())
	if daemonErr == nil {
		steps = append(steps, []string{"enable", serviceUnitName}, []string{"restart", serviceUnitName})
	}
	steps = append(steps, []string{"enable", "--now", syncTimerName})
	for _, args := range steps {
		if out, err := systemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if daemonErr == nil {
		fmt.Fprintf(w, "%s enabled and (re)started\n", serviceUnitName)
	}
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
