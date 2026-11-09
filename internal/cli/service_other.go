//go:build !linux

package cli

import "io"

// systemdUser is false: the units are systemd-only.
func systemdUser() bool { return false }

// applyUnits prints the units: there is no systemd here.
func applyUnits(w io.Writer, u serviceUnits, _ bool) error {
	printUnits(w, u)
	return errNoSystemd
}

func uninstallUnit(io.Writer) error { return errNoSystemd }

func restartUnit(io.Writer) error { return errNoSystemd }

func unitState() string { return "unsupported" }

func syncTimerState() string { return "unsupported" }
