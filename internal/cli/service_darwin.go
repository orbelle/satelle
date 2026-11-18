//go:build darwin

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var launchctl launchRunner = func(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func launchAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

func applyUnits(w io.Writer, u serviceUnits, force bool) error {
	if !force {
		fmt.Fprintln(w, "service refresh on macOS is not supported yet; run 'satelle service install' to write and load the launch agents")
		return errNoSystemd
	}
	return installLaunchAgents(w, launchctl, launchAgentsDir(), os.Getuid(), launchSpec{Daemon: u.DaemonBin, Addr: u.Addr, Port: u.Port, CLI: u.CLI})
}

func uninstallUnit(w io.Writer) error {
	return uninstallLaunchAgents(w, launchctl, launchAgentsDir(), os.Getuid())
}

func restartUnit(io.Writer) error {
	return errors.New("satelle service restart is not supported on macOS yet; run 'satelle service install' to reload the launch agents")
}

func launchState(label string) string {
	if _, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)); err != nil {
		return "inactive"
	}
	return "active"
}

func unitState() string { return launchState(launchDaemonLabel) }

func syncTimerState() string { return launchState(launchSyncLabel) }

func unitPath() string { return launchPlistPath(launchAgentsDir(), launchDaemonLabel) }

func syncUnitPath() string { return launchPlistPath(launchAgentsDir(), launchSyncLabel) }

func daemonUnitDesc() string { return "the satelled launch agent (" + unitPath() + ")" }

func syncUnitDesc() string { return "the sync launch agent (" + syncUnitPath() + ")" }

func installedDaemon(body string) string { return plistProgram(body) }
