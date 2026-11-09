//go:build !linux

package cli

import (
	"errors"
	"fmt"
	"io"
)

var errNoSystemd = errors.New("the satelled service unit is systemd-only; run satelled directly on this platform")

func installUnit(w io.Writer, u serviceUnits) error {
	fmt.Fprintf(w, "no systemd here; run satelled directly. The unit would be:\n\n%s", u.Daemon)
	fmt.Fprintf(w, "\nand run satelle ledger autosync every %d s. The sync units would be:\n\n%s\n%s",
		int(syncPeriod.Seconds()), u.SyncService, u.SyncTimer)
	return errNoSystemd
}

func uninstallUnit(io.Writer) error { return errNoSystemd }

func restartUnit(io.Writer) error { return errNoSystemd }

func unitState() string { return "unsupported" }

func syncTimerState() string { return "unsupported" }
