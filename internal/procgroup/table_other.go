//go:build !linux

package procgroup

import (
	"fmt"
	"runtime"
)

type Entry struct {
	PID, PPID int
	Argv      []string
	Comm      string
}

func Table() ([]Entry, error) {
	return nil, fmt.Errorf("the process table is unavailable on %s", runtime.GOOS)
}
