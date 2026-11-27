//go:build linux

package cli

import (
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

func TestCapacitySamplesOnce(t *testing.T) {
	isolate(t)
	must(t, "init")
	calls := 0
	old := takeSample
	t.Cleanup(func() { takeSample = old })
	takeSample = func(k capacity.Known, tab func() ([]procgroup.Entry, error)) capacity.Sample {
		calls++
		return old(k, tab)
	}
	out := must(t, "capacity")
	if calls != 1 {
		t.Errorf("capacity sampled %d times, want 1:\n%s", calls, out)
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
	if diff := sameDigest(before, digest(t, home)); len(diff) != 0 {
		t.Errorf("capacity wrote under the satelle home: %v:\n%s", diff, out)
	}
}
