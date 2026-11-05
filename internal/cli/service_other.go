//go:build !linux

package cli

import (
	"errors"
	"fmt"
	"io"
)

var errNoSystemd = errors.New("the satelled service unit is systemd-only; run satelled directly on this platform")

func installUnit(w io.Writer, unit string) error {
	fmt.Fprintf(w, "no systemd here; run satelled directly. The unit would be:\n\n%s", unit)
	return errNoSystemd
}

func uninstallUnit(io.Writer) error { return errNoSystemd }

func restartUnit(io.Writer) error { return errNoSystemd }

func unitState() string { return "unsupported" }
