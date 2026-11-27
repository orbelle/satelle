package capacity

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

func TestParseMeminfo(t *testing.T) {
	in := "MemTotal:       65536000 kB\nMemFree:         1000 kB\nMemAvailable:   40000000 kB\nBuffers: 1 kB\n"
	total, avail, ok := parseMeminfo(in)
	if !ok || total != 65536000*1024 || avail != 40000000*1024 {
		t.Fatalf("got %d %d %v", total, avail, ok)
	}
	if _, _, ok := parseMeminfo("MemTotal: 1 kB\n"); ok {
		t.Error("meminfo without MemAvailable parsed")
	}
}

func TestParseLoadavg(t *testing.T) {
	l, ok := parseLoadavg("1.20 0.98 0.75 2/1234 5678\n")
	if !ok || l != [3]float64{1.20, 0.98, 0.75} {
		t.Fatalf("got %v %v", l, ok)
	}
	if _, ok := parseLoadavg("1.0 x"); ok {
		t.Error("short loadavg parsed")
	}
}

func TestCountAgents(t *testing.T) {
	known := Known{"claude": "claude", "pi": "pi", "grok": "grok", "cursor-agent": "cursor"}
	tab := []procgroup.Entry{
		{PID: 10, PPID: 1, Argv: []string{"/usr/bin/claude", "--resume"}, Comm: "claude"},
		{PID: 11, PPID: 10, Argv: []string{"claude", "helper"}, Comm: "claude"},
		{PID: 20, PPID: 1, Argv: []string{"/usr/bin/node", "/opt/x/pi", "run"}, Comm: "node"},
		{PID: 30, PPID: 1, Argv: []string{"/usr/bin/node", "/opt/y/main.js"}, Comm: "grok"},
		{PID: 40, PPID: 1, Argv: []string{"vim", "claude.go"}, Comm: "vim"},
		{PID: 50, PPID: 11, Argv: []string{"/usr/bin/claude"}, Comm: "claude"},
		{PID: 60, PPID: 1, Argv: []string{"/opt/bin/Claude.EXE"}, Comm: ""},
	}
	got := CountAgents(tab, known)
	want := map[string]int{"claude": 2, "pi": 1, "grok": 1, "cursor": 0}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, want %d (%v)", k, got[k], v, got)
		}
	}
}

func TestTakeFillsEveryFigure(t *testing.T) {
	s := Take(Known{"x-no-such-harness": "x"})
	if s.Cores < 1 {
		t.Errorf("cores %d", s.Cores)
	}
	if _, ok := s.Agents["x"]; !ok {
		t.Errorf("agents %v lacks the known label", s.Agents)
	}
	if s.Errs[FigMemory] == nil && (s.MemTotal == 0 || s.MemAvailable > s.MemTotal) {
		t.Errorf("memory %d of %d", s.MemAvailable, s.MemTotal)
	}
}

func TestCapacityStartsNoProcess(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, imp := range strings.Fields(string(out)) {
		switch {
		case imp == "os/exec":
			t.Error("capacity imports os/exec: sampling must start no process")
		case strings.HasPrefix(imp, "github.com/bobmcallan/satelle/internal/") && imp != "github.com/bobmcallan/satelle/internal/procgroup":
			t.Errorf("capacity imports %s: it takes its harnesses from the caller", imp)
		}
	}
}
