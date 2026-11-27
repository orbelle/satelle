//go:build !linux

package procgroup

import (
	"fmt"
	"runtime"
)

// Entry is one live process of the process table: its pid, its parent, its
// argv and its kernel command name.
type Entry struct {
	PID, PPID int
	Argv      []string
	Comm      string
}

// Table is unavailable off Linux.
func Table() ([]Entry, error) {
	return nil, fmt.Errorf("the process table is unavailable on %s", runtime.GOOS)
}
