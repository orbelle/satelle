//go:build !linux && !darwin

package cli

import "io"

func applyUnits(w io.Writer, u serviceUnits, _ bool) error {
	printUnits(w, u)
	return errNoSystemd
}

func uninstallUnit(io.Writer) error { return errNoSystemd }

func restartUnit(io.Writer) error { return errNoSystemd }

func unitState() string { return "unsupported" }

func syncTimerState() string { return "unsupported" }
