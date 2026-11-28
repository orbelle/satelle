//go:build linux

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/capacity"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

func capacityLine(t *testing.T, out, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no %s line in:\n%s", name, out)
	return ""
}

func fakeTable(t *testing.T, tab ...procgroup.Entry) {
	t.Helper()
	old := processTable
	t.Cleanup(func() { processTable = old })
	processTable = func() ([]procgroup.Entry, error) { return tab, nil }
}

func figure(t *testing.T, out, name, pattern string) {
	t.Helper()
	got := capacityLine(t, out, name)
	if !regexp.MustCompile(`^(` + pattern + `|unavailable: .+)$`).MatchString(got) {
		t.Errorf("%s line %q is neither the figure nor unavailable:\n%s", name, got, out)
	}
}

func children(t *testing.T) []string {
	t.Helper()
	tasks, _ := filepath.Glob("/proc/self/task/*/children")
	var out []string
	for _, p := range tasks {
		b, err := os.ReadFile(p)
		if err == nil {
			out = append(out, strings.Fields(string(b))...)
		}
	}
	slices.Sort(out)
	return out
}

func TestCapacityReportsTheMachine(t *testing.T) {
	f := newDoctorFixture(t)
	ids := fourSeats(t)
	must(t, "project", "add")
	fakeTable(t,
		procgroup.Entry{PID: 1, PPID: 0, Argv: []string{"/sbin/init"}, Comm: "init"},
		procgroup.Entry{PID: 1 << 30, PPID: 1, Argv: []string{"/opt/grok/grok", "--session"}, Comm: "grok"},
		procgroup.Entry{PID: 1<<30 + 1, PPID: 1 << 30, Argv: []string{"/opt/grok/grok", "helper"}, Comm: "grok"},
	)

	out := must(t, "capacity")
	if got := capacityLine(t, out, "cores"); got != strconv.Itoa(runtime.NumCPU()) {
		t.Errorf("cores %q, want %d:\n%s", got, runtime.NumCPU(), out)
	}
	figure(t, out, "memory", `[0-9.]+ GiB total, [0-9.]+ GiB available`)
	figure(t, out, "load", `[0-9.]+ [0-9.]+ [0-9.]+ \(1, 5, 15 min\)`)
	agents := capacityLine(t, out, "agents")
	for _, h := range hook.Harnesses {
		if !regexp.MustCompile(`(^|, )` + h + ` [0-9]+`).MatchString(agents) {
			t.Errorf("agents line %q lacks harness %s:\n%s", agents, h, out)
		}
	}
	if n := regexp.MustCompile(`grok ([0-9]+)`).FindStringSubmatch(agents); n == nil || n[1] != "1" {
		t.Errorf("agents line %q does not count the one grok session in the table:\n%s", agents, out)
	}
	if s := capacityLine(t, out, "seats"); s != "1 engaged here" {
		t.Errorf("seats %q, want 1 engaged here\n%s", s, out)
	}
	if !strings.Contains(out, ids[seatlife.Here]+"  s-here  "+f.dir) {
		t.Errorf("output lacks the seat here (%s s-here %s):\n%s", ids[seatlife.Here], f.dir, out)
	}
	for _, st := range []seatlife.Standing{seatlife.Elsewhere, seatlife.Gone, seatlife.Unverified} {
		if strings.Contains(out, ids[st]) {
			t.Errorf("output lists the %s seat %s:\n%s", st, ids[st], out)
		}
	}

	other := otherRepo(t, f.dir)
	t.Chdir(other)
	oid := createID(t, "story", "create", "--title", "other project", "--criteria", "c")
	must(t, "story", "seat", oid, "--session", "s-other")
	if err := seatlife.Own(config.GlobalDir(), "s-other", procgroup.Self()); err != nil {
		t.Fatalf("own s-other: %v\n%s", err, out)
	}
	must(t, "project", "add")
	t.Chdir(f.dir)
	out = must(t, "capacity")
	if s := capacityLine(t, out, "seats"); s != "2 engaged here" {
		t.Errorf("seats %q, want 2 engaged here across projects\n%s", s, out)
	}
	if !strings.Contains(out, oid+"  s-other  "+other) {
		t.Errorf("output lacks the other project's seat:\n%s", out)
	}
}

func TestCapacityReusesItsSampleWithinTheCadence(t *testing.T) {
	isolate(t)
	must(t, "init")
	fakeTable(t, procgroup.Entry{PID: 1, PPID: 0, Argv: []string{"/sbin/init"}, Comm: "init"})
	calls := 0
	old := readMachine
	t.Cleanup(func() { readMachine = old })
	readMachine = func(tab func() ([]procgroup.Entry, error)) capacity.Snapshot {
		calls++
		return old(tab)
	}
	first := must(t, "capacity")
	second := must(t, "capacity")
	if calls != 1 {
		t.Errorf("two runs within the cadence sampled %d times, want 1:\n%s\n%s", calls, first, second)
	}
	if a, b := capacityLine(t, first, "sampled"), capacityLine(t, second, "sampled"); a != b {
		t.Errorf("second run's sampled line %q, want the first run's %q", b, a)
	}

	path := filepath.Join(config.GlobalDir(), capacitySampleFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no kept sample: %v", err)
	}
	var sn capacity.Snapshot
	if err := json.Unmarshal(b, &sn); err != nil {
		t.Fatalf("kept sample: %v", err)
	}
	sn.TakenAt = sn.TakenAt.Add(-capacity.Cadence)
	if b, err = json.Marshal(sn); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	third := must(t, "capacity")
	if calls != 2 {
		t.Errorf("a run after the cadence sampled %d times in all, want 2:\n%s", calls, third)
	}
}

func TestCapacityStartsNoProcess(t *testing.T) {
	isolate(t)
	must(t, "init")
	must(t, "project", "add")
	home := config.GlobalDir()
	before, kids := digest(t, home), children(t)
	out := must(t, "capacity")
	if after := children(t); !slices.Equal(after, kids) {
		t.Errorf("children %v after the run, want %v:\n%s", after, kids, out)
	}
	kept := filepath.Join(home, capacitySampleFile)
	for _, p := range sameDigest(before, digest(t, home)) {
		if p != home && p != kept && p != kept+" (new)" {
			t.Errorf("capacity wrote %s under the satelle home:\n%s", p, out)
		}
	}
}

func ceilingTable(t *testing.T) {
	t.Helper()
	fakeTable(t,
		procgroup.Entry{PID: 1, PPID: 0, Argv: []string{"/sbin/init"}, Comm: "init"},
		procgroup.Entry{PID: 1 << 30, PPID: 1, Argv: []string{"/opt/grok/grok", "--session"}, Comm: "grok"},
	)
}

func TestCapacityShowsTheWorkerCeiling(t *testing.T) {
	isolate(t)
	must(t, "init")
	ceilingTable(t)
	if err := os.MkdirAll(config.GlobalDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GlobalConfigPath(), []byte("[capacity]\nworkers = 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := regexp.MustCompile(`\(1 in all on this machine; ceiling 4\)$`)
	out := must(t, "capacity")
	if agents := capacityLine(t, out, "agents"); !want.MatchString(agents) {
		t.Errorf("agents line %q does not show the ceiling of 4 beside the one live agent:\n%s", agents, out)
	}
	must(t, "project", "add")
	out = must(t, "capacity")
	if agents := capacityLine(t, out, "agents"); !want.MatchString(agents) {
		t.Errorf("after a save of config.toml, agents line %q lost the ceiling of 4:\n%s", agents, out)
	}
	if s := must(t, "settings"); !strings.Contains(s, `capacity.workers  "4"`) {
		t.Errorf("settings does not show capacity.workers 4:\n%s", s)
	}
}

func TestCapacityShowsNoCeilingByDefault(t *testing.T) {
	isolate(t)
	must(t, "init")
	ceilingTable(t)
	noCeiling := func(when string) {
		t.Helper()
		out := must(t, "capacity")
		agents := capacityLine(t, out, "agents")
		if !strings.HasSuffix(agents, "; no ceiling)") || regexp.MustCompile(`ceiling [0-9]`).MatchString(agents) {
			t.Errorf("%s: agents line %q does not show no ceiling:\n%s", when, agents, out)
		}
	}
	if _, err := os.Stat(config.GlobalConfigPath()); err == nil {
		t.Fatalf("init wrote %s; this test needs it absent", config.GlobalConfigPath())
	}
	noCeiling("no config.toml")
	must(t, "settings", "set", "capacity.workers", "2")
	must(t, "settings", "set", "capacity.workers", "")
	noCeiling("ceiling set then cleared")
	for _, bad := range []string{"-1", "0", "x"} {
		out, err := run(t, "", "settings", "set", "capacity.workers", "--", bad)
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(bad)) {
			t.Errorf("settings set capacity.workers %s: err %v, want a refusal naming the value\n%s", bad, err, out)
		}
	}
	noCeiling("after refused values")
}
