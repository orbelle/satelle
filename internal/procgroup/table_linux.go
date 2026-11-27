//go:build linux

package procgroup

import (
	"os"
	"strconv"
	"strings"
)

// Entry is one live process of the process table: its pid, its parent, its
// argv and its kernel command name.
type Entry struct {
	PID, PPID int
	Argv      []string
	Comm      string
}

// Table reads every live process once from /proc. A process that exits
// mid-read, or is a zombie, is left out.
func Table() ([]Entry, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range ents {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || !d.IsDir() {
			continue
		}
		base := "/proc/" + d.Name() + "/"
		st, err := os.ReadFile(base + "stat")
		if err != nil {
			continue
		}
		state, ppid, _, ok := parseProcStat(string(st))
		if !ok || state == "Z" || state == "X" {
			continue
		}
		e := Entry{PID: pid, PPID: ppid}
		if b, err := os.ReadFile(base + "cmdline"); err == nil {
			for a := range strings.SplitSeq(strings.TrimRight(string(b), "\x00"), "\x00") {
				if a != "" {
					e.Argv = append(e.Argv, a)
				}
			}
		}
		if b, err := os.ReadFile(base + "comm"); err == nil {
			e.Comm = strings.TrimSpace(string(b))
		}
		out = append(out, e)
	}
	return out, nil
}
