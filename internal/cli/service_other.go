//go:build !linux && !darwin

package cli

import (
	"fmt"
	"io"
)

const (
	daemonDefName  = serviceUnitName
	syncDefName    = syncTimerName
	defNoun        = "unit"
	serviceManaged = false
)

func applyService(w io.Writer, s serviceSpec, force bool) error {
	return applyUnits(w, unitsFor(s), force)
}

func applyUnits(w io.Writer, u serviceUnits, _ bool) error {
	printUnits(w, u)
	return errNoSystemd
}

func uninstallUnit(io.Writer) error { return errNoSystemd }

func restartUnit(io.Writer) error { return errNoSystemd }

func unitState() string { return "unsupported" }

func syncTimerState() string { return "unsupported" }

func printManagerStatus(w io.Writer) {
	fmt.Fprintf(w, "unit      %s (%s)\n", unitPath(), unitState())
	fmt.Fprintf(w, "sync      %s (%s)\n", syncTimerName, syncTimerState())
}

func daemonDefPath() string { return unitPath() }

func defDaemon(body string) string { return unitDaemon(body) }

func syncDefPath() string { return userUnitPath(syncTimerName) }

func syncTimerLive(string) bool { return false }
