// Package capacity samples this machine's capacity and load once, when asked:
// cores, memory, load average and the coding-agent sessions running on it.
// It stores nothing and starts no process.
package capacity

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// Known maps an executable basename (lower case, no .exe) to a harness label.
type Known map[string]string

// Sample is one reading of every figure. A figure that could not be read has
// its error under Errs, keyed memory, load or agents.
type Sample struct {
	Cores        int
	MemTotal     uint64
	MemAvailable uint64
	Load         [3]float64
	Agents       map[string]int
	Errs         map[string]error
}

const (
	FigMemory = "memory"
	FigLoad   = "load"
	FigAgents = "agents"
)

// Take reads each figure once.
func Take(known Known) Sample {
	s := Sample{Cores: runtime.NumCPU(), Errs: map[string]error{}}
	var err error
	if s.MemTotal, s.MemAvailable, err = readMemory(); err != nil {
		s.Errs[FigMemory] = err
	}
	if s.Load, err = readLoad(); err != nil {
		s.Errs[FigLoad] = err
	}
	tab, err := procgroup.Table()
	if err != nil {
		s.Errs[FigAgents] = err
	}
	s.Agents = CountAgents(tab, known)
	return s
}

var interpreters = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true}

func base(p string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".exe")
}

func label(e procgroup.Entry, known Known) (string, bool) {
	var cands []string
	if len(e.Argv) > 0 {
		b := base(e.Argv[0])
		cands = append(cands, b)
		if interpreters[b] && len(e.Argv) > 1 {
			cands = append(cands, base(e.Argv[1]))
		}
	}
	if e.Comm != "" {
		cands = append(cands, base(e.Comm))
	}
	for _, c := range cands {
		if l, ok := known[c]; ok {
			return l, true
		}
	}
	return "", false
}

// CountAgents counts the sessions of each known harness in the table: a
// matching process whose parent matches the same label is a helper of that
// session and is not counted again. Every label of known is in the result.
func CountAgents(tab []procgroup.Entry, known Known) map[string]int {
	out := map[string]int{}
	for _, l := range known {
		out[l] = 0
	}
	labels := map[int]string{}
	for _, e := range tab {
		if l, ok := label(e, known); ok {
			labels[e.PID] = l
		}
	}
	for _, e := range tab {
		l, ok := labels[e.PID]
		if !ok {
			continue
		}
		if pl, ok := labels[e.PPID]; ok && pl == l {
			continue
		}
		out[l]++
	}
	return out
}

func parseMeminfo(s string) (total, avail uint64, ok bool) {
	var haveT, haveA bool
	for line := range strings.SplitSeq(s, "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		switch k {
		case "MemTotal":
			total, haveT = n, true
		case "MemAvailable":
			avail, haveA = n, true
		}
	}
	return total, avail, haveT && haveA
}

func parseLoadavg(s string) (l [3]float64, ok bool) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return l, false
	}
	for i := range 3 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return l, false
		}
		l[i] = v
	}
	return l, true
}
