//go:build linux

package procgroup

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func osProbe(pid int) (Liveness, string) {
	if signalLiveness(pid) == Gone {
		return Gone, ""
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Gone, ""
		}
		return Alive, ""
	}
	state, _, start, ok := parseProcStat(string(b))
	if !ok {
		return Alive, ""
	}
	if state == "Z" || state == "X" {
		return Gone, ""
	}
	return Alive, start
}

func parseProcStat(line string) (state string, ppid int, start string, ok bool) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return "", 0, "", false
	}
	f := strings.Fields(line[i+1:])
	if len(f) < 20 {
		return "", 0, "", false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return "", 0, "", false
	}
	return f[0], ppid, f[19], true
}
