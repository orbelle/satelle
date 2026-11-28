package capacity

import (
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

type Known map[string]string

type Sample struct {
	TakenAt      time.Time
	Cores        int
	MemTotal     uint64
	MemAvailable uint64
	Load         [3]float64
	Agents       map[string]int
	Errs         map[string]error
}

type Proc struct {
	PID   int      `json:"pid"`
	PPID  int      `json:"ppid"`
	Names []string `json:"names,omitempty"`
}

type Snapshot struct {
	TakenAt      time.Time         `json:"taken_at"`
	Cores        int               `json:"cores"`
	MemTotal     uint64            `json:"mem_total"`
	MemAvailable uint64            `json:"mem_available"`
	Load         [3]float64        `json:"load"`
	Errs         map[string]string `json:"errs,omitempty"`
	Procs        []Proc            `json:"procs,omitempty"`
}

const (
	FigMemory = "memory"
	FigLoad   = "load"
	FigAgents = "agents"
)

func Read(table func() ([]procgroup.Entry, error)) Snapshot {
	sn := Snapshot{TakenAt: time.Now(), Cores: runtime.NumCPU(), Errs: map[string]string{}}
	var err error
	if sn.MemTotal, sn.MemAvailable, err = readMemory(); err != nil {
		sn.Errs[FigMemory] = err.Error()
	}
	if sn.Load, err = readLoad(); err != nil {
		sn.Errs[FigLoad] = err.Error()
	}
	tab, err := table()
	if err != nil {
		sn.Errs[FigAgents] = err.Error()
	}
	for _, e := range tab {
		if names := candidates(e.Argv, e.Comm); len(names) > 0 {
			sn.Procs = append(sn.Procs, Proc{PID: e.PID, PPID: e.PPID, Names: names})
		}
	}
	return sn
}

func (sn Snapshot) Sample(known Known) Sample {
	s := Sample{
		TakenAt:      sn.TakenAt,
		Cores:        sn.Cores,
		MemTotal:     sn.MemTotal,
		MemAvailable: sn.MemAvailable,
		Load:         sn.Load,
		Agents:       CountAgents(sn.Procs, known),
		Errs:         map[string]error{},
	}
	for fig, msg := range sn.Errs {
		s.Errs[fig] = errors.New(msg)
	}
	return s
}

var interpreters = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true}

const maxName = 64

func base(p string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".exe")
}

func isName(s string) bool {
	if s == "" || len(s) > maxName || s[0] == '-' {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '+', c == '-':
		default:
			return false
		}
	}
	return true
}

func candidates(argv []string, comm string) []string {
	var raw []string
	if len(argv) > 0 {
		b := base(argv[0])
		raw = append(raw, b)
		if interpreters[b] && len(argv) > 1 && !strings.HasPrefix(argv[1], "-") {
			raw = append(raw, base(argv[1]))
		}
	}
	if comm != "" {
		raw = append(raw, base(comm))
	}
	var out []string
	for _, c := range raw {
		if isName(c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

func label(p Proc, known Known) (string, bool) {
	for _, c := range p.Names {
		if l, ok := known[c]; ok {
			return l, true
		}
	}
	return "", false
}

func CountAgents(procs []Proc, known Known) map[string]int {
	out := map[string]int{}
	for _, l := range known {
		out[l] = 0
	}
	labels := map[int]string{}
	for _, p := range procs {
		if l, ok := label(p, known); ok {
			labels[p.PID] = l
		}
	}
	for _, p := range procs {
		l, ok := labels[p.PID]
		if !ok {
			continue
		}
		if pl, ok := labels[p.PPID]; ok && pl == l {
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
