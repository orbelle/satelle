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

func installUnit(w io.Writer, unit string) error {
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s\n", path)
	for _, args := range [][]string{{"daemon-reload"}, {"enable", serviceUnitName}, {"restart", serviceUnitName}} {
		if out, err := systemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Fprintf(w, "%s enabled and started\n", serviceUnitName)
	return nil
}

func uninstallUnit(w io.Writer) error {
	_, _ = systemctl("disable", "--now", serviceUnitName)
	path := unitPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = systemctl("daemon-reload")
	fmt.Fprintf(w, "removed %s\n", path)
	return nil
}

func restartUnit(w io.Writer) error {
	if out, err := systemctl("restart", serviceUnitName); err != nil {
		return fmt.Errorf("systemctl --user restart %s: %v: %s", serviceUnitName, err, out)
	}
	fmt.Fprintf(w, "%s restarted\n", serviceUnitName)
	return nil
}

func unitState() string {
	out, _ := systemctl("is-active", serviceUnitName)
	if out == "" {
		return "unknown"
	}
	return out
}
