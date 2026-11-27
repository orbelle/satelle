//go:build linux

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

const (
	daemonDefName  = serviceUnitName
	syncDefName    = syncTimerName
	defNoun        = "unit"
	serviceManaged = true
)

func applyService(w io.Writer, s serviceSpec, force bool) error {
	return applyUnits(w, unitsFor(s), force)
}

func systemctl(args ...string) (string, error) {
	if testing.Testing() && (os.Getenv("XDG_RUNTIME_DIR") != "" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "") {
		panic("satelle: a test reached systemctl --user with the user manager reachable; unset XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS (internal/cli TestMain) and put a fake systemctl on PATH")
	}
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func systemdUser() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := systemctl("show-environment")
	return err == nil
}

func applyUnits(w io.Writer, u serviceUnits, force bool) error {
	if !systemdUser() {
		printUnits(w, u)
		return errNoSystemd
	}
	for _, f := range []struct{ name, body string }{
		{serviceUnitName, u.Daemon}, {syncServiceName, u.SyncService}, {syncTimerName, u.SyncTimer}, {followServiceName, u.Follow},
	} {
		if _, err := writeDefinition(w, userUnitPath(f.name), f.body, force, unitDaemon); err != nil {
			return err
		}
	}
	steps := [][]string{{"daemon-reload"}}
	_, daemonErr := os.Stat(unitPath())
	if daemonErr == nil {
		steps = append(steps, []string{"enable", serviceUnitName}, []string{"restart", serviceUnitName})
	}
	steps = append(steps, []string{"enable", "--now", syncTimerName}, []string{"enable", followServiceName}, []string{"restart", followServiceName})
	for _, args := range steps {
		if out, err := systemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if daemonErr == nil {
		fmt.Fprintf(w, "%s enabled and (re)started\n", serviceUnitName)
	}
	fmt.Fprintf(w, "%s enabled and started: each project on this machine syncs every %s\n", syncTimerName, mirror.SyncPeriod)
	fmt.Fprintf(w, "%s enabled and (re)started: writes push and other locations' changes pull within seconds\n", followServiceName)
	return nil
}

func uninstallUnit(w io.Writer) error {
	_, _ = systemctl("disable", "--now", followServiceName)
	_, _ = systemctl("disable", "--now", syncTimerName)
	_, _ = systemctl("disable", "--now", serviceUnitName)
	for _, name := range []string{followServiceName, syncTimerName, syncServiceName, serviceUnitName} {
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

func printManagerStatus(w io.Writer) {
	fmt.Fprintf(w, "unit      %s (%s)\n", unitPath(), unitState())
	fmt.Fprintf(w, "sync      %s (%s)\n", syncTimerName, syncTimerState())
	fmt.Fprintf(w, "follow    %s (%s)\n", followServiceName, activeState(followServiceName))
}

func daemonDefPath() string { return unitPath() }

func defDaemon(body string) string { return unitDaemon(body) }

func syncDefPath() string { return userUnitPath(syncTimerName) }

func syncTimerLive(state string) bool { return state == "active" }
