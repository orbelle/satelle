//go:build linux

package procgroup

import (
	"os"
	"strconv"
)

// Ancestors is pid's chain of parents, nearest first, excluding pid itself and
// init, at most maxAncestors long; each with its start identity.
func Ancestors(pid int) []Proc {
	var out []Proc
	for len(out) < maxAncestors {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return out
		}
		_, ppid, _, ok := parseProcStat(string(b))
		if !ok || ppid <= 1 {
			return out
		}
		pb, err := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/stat")
		if err != nil {
			return out
		}
		_, _, start, ok := parseProcStat(string(pb))
		if !ok {
			return out
		}
		out = append(out, Proc{PID: ppid, Start: start})
		pid = ppid
	}
	return out
}
