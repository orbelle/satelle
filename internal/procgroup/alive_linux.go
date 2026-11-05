//go:build linux

package procgroup

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// osProbe is the signal-0 existence check plus the process's start time from
// /proc. A zombie — exited, not yet reaped — is gone: it can
// never record a result.
func osProbe(pid int) (Liveness, string) {
	if signalLiveness(pid) == Gone {
		return Gone, ""
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Gone, ""
		}
		return Alive, "" // alive, but /proc will not say who it is
	}
	state, start, ok := parseProcStat(string(b))
	if !ok {
		return Alive, ""
	}
	if state == "Z" || state == "X" {
		return Gone, ""
	}
	return Alive, start
}

// parseProcStat reads the state (field 3) and start time (field 22) of a
// /proc/<pid>/stat line. The command (field 2) is parenthesised and may itself
// hold spaces and parentheses, so fields are counted from the last ')'.
func parseProcStat(line string) (state, start string, ok bool) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return "", "", false
	}
	f := strings.Fields(line[i+1:])
	// f[0] is field 3 (state); field 22 is f[19].
	if len(f) < 20 {
		return "", "", false
	}
	return f[0], f[19], true
}
