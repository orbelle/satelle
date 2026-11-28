package capacity

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func procs(tab ...procgroup.Entry) []Proc {
	var out []Proc
	for _, e := range tab {
		out = append(out, Proc{PID: e.PID, PPID: e.PPID, Names: candidates(e.Argv, e.Comm)})
	}
	return out
}

func TestCountAgents(t *testing.T) {
	known := Known{"claude": "claude", "pi": "pi", "grok": "grok", "cursor-agent": "cursor"}
	tab := procs(
		procgroup.Entry{PID: 10, PPID: 1, Argv: []string{"/usr/bin/claude", "--resume"}, Comm: "claude"},
		procgroup.Entry{PID: 11, PPID: 10, Argv: []string{"claude", "helper"}, Comm: "claude"},
		procgroup.Entry{PID: 20, PPID: 1, Argv: []string{"/usr/bin/node", "/opt/x/pi", "run"}, Comm: "node"},
		procgroup.Entry{PID: 30, PPID: 1, Argv: []string{"/usr/bin/node", "/opt/y/main.js"}, Comm: "grok"},
		procgroup.Entry{PID: 40, PPID: 1, Argv: []string{"vim", "claude.go"}, Comm: "vim"},
		procgroup.Entry{PID: 50, PPID: 11, Argv: []string{"/usr/bin/claude"}, Comm: "claude"},
		procgroup.Entry{PID: 60, PPID: 1, Argv: []string{"/opt/bin/Claude.EXE"}, Comm: ""},
	)
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

func TestCandidatesKeepOnlyNames(t *testing.T) {
	long := strings.Repeat("a", maxName+1)
	cases := []struct {
		argv []string
		comm string
		want []string
	}{
		{[]string{"node", "/usr/lib/claude/cli.js"}, "node", []string{"node", "cli.js"}},
		{[]string{"python3", "-cimport os; TOKEN='x'"}, "python3", []string{"python3"}},
		{[]string{"node", "-e", "console.log(1)"}, "node", []string{"node"}},
		{[]string{"node", "--eval=secret/x"}, "node", []string{"node"}},
		{[]string{"/opt/my tool"}, "c@t", nil},
		{[]string{long}, long, nil},
		{[]string{"/usr/bin/Grok"}, "grok", []string{"grok"}},
	}
	for _, c := range cases {
		if got := candidates(c.argv, c.comm); !slices.Equal(got, c.want) {
			t.Errorf("candidates(%q, %q) = %q, want %q", c.argv, c.comm, got, c.want)
		}
	}
}

func TestReadFillsEveryFigure(t *testing.T) {
	sn := Read(func() ([]procgroup.Entry, error) {
		return []procgroup.Entry{{PID: 7, PPID: 1, Argv: []string{"/bin/x-tool"}, Comm: "x-tool"}}, nil
	})
	if sn.TakenAt.IsZero() {
		t.Error("snapshot has no time")
	}
	s := sn.Sample(Known{"x-tool": "x", "x-no-such-harness": "y"})
	if s.Cores < 1 {
		t.Errorf("cores %d", s.Cores)
	}
	if s.Agents["x"] != 1 {
		t.Errorf("agents %v, want x 1", s.Agents)
	}
	if _, ok := s.Agents["y"]; !ok {
		t.Errorf("agents %v lacks the known label", s.Agents)
	}
	if s.Errs[FigMemory] == nil && (s.MemTotal == 0 || s.MemAvailable > s.MemTotal) {
		t.Errorf("memory %d of %d", s.MemAvailable, s.MemTotal)
	}
	failed := Read(func() ([]procgroup.Entry, error) { return nil, errors.New("no table") }).Sample(Known{})
	if err := failed.Errs[FigAgents]; err == nil || err.Error() != "no table" {
		t.Errorf("agents error %v, want no table", err)
	}
}

func TestReuseWithinCadence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "capacity-sample.json")
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	reads := 0
	stamp := t0
	read := func() Snapshot {
		reads++
		return Snapshot{TakenAt: stamp, Cores: reads, Procs: []Proc{{PID: 9, PPID: 1, Names: []string{"grok"}}}}
	}

	first, err := Reuse(path, t0, read)
	if err != nil || reads != 1 {
		t.Fatalf("first: reads %d, err %v", reads, err)
	}
	second, err := Reuse(path, t0.Add(Cadence/2), read)
	if err != nil || reads != 1 {
		t.Fatalf("within the cadence: reads %d, err %v", reads, err)
	}
	if !second.TakenAt.Equal(first.TakenAt) || second.Cores != first.Cores || !slices.EqualFunc(second.Procs, first.Procs, func(a, b Proc) bool {
		return a.PID == b.PID && a.PPID == b.PPID && slices.Equal(a.Names, b.Names)
	}) {
		t.Errorf("reused %+v, want %+v", second, first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sample mode %v, want 0600", info.Mode().Perm())
	}

	stamp = t0.Add(Cadence)
	if _, err := Reuse(path, t0.Add(Cadence), read); err != nil || reads != 2 {
		t.Fatalf("at the cadence: reads %d, err %v", reads, err)
	}
	stamp = t0
	if _, err := Reuse(path, t0, read); err != nil || reads != 3 {
		t.Fatalf("future-dated sample: reads %d, err %v", reads, err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reuse(path, t0, read); err != nil || reads != 4 {
		t.Fatalf("corrupt sample: reads %d, err %v", reads, err)
	}
	if _, err := Reuse(path, t0.Add(time.Second), read); err != nil || reads != 4 {
		t.Fatalf("after overwriting the corrupt sample: reads %d, err %v", reads, err)
	}
	left, _ := filepath.Glob(path + ".*")
	if len(left) != 0 {
		t.Errorf("temp files left: %v", left)
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
