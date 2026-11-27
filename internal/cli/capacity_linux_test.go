//go:build linux

package cli

import (
	"os"
	"os/exec"
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

func fakeHarness(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep to stand in for a harness")
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
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
	fakeHarness(t, "grok")

	out := must(t, "capacity")
	if got := capacityLine(t, out, "cores"); got != strconv.Itoa(runtime.NumCPU()) {
		t.Errorf("cores %q, want %d", got, runtime.NumCPU())
	}
	if m := capacityLine(t, out, "memory"); !regexp.MustCompile(`^[0-9.]+ GiB total, [0-9.]+ GiB available$`).MatchString(m) {
		t.Errorf("memory line %q", m)
	}
	if l := capacityLine(t, out, "load"); !regexp.MustCompile(`^[0-9.]+ [0-9.]+ [0-9.]+ \(1, 5, 15 min\)$`).MatchString(l) {
		t.Errorf("load line %q", l)
	}
	agents := capacityLine(t, out, "agents")
	for _, h := range hook.Harnesses {
		if !regexp.MustCompile(`(^|, )` + h + ` [0-9]+`).MatchString(agents) {
			t.Errorf("agents line %q lacks harness %s", agents, h)
		}
	}
	if n := regexp.MustCompile(`grok ([0-9]+)`).FindStringSubmatch(agents); n == nil || n[1] == "0" {
		t.Errorf("agents line %q does not count the running grok", agents)
	}
	if s := capacityLine(t, out, "seats"); s != "1 engaged here" {
		t.Errorf("seats %q, want 1 engaged here\n%s", s, out)
	}
	if !strings.Contains(out, ids[seatlife.Here]+"  s-here  "+f.dir) {
		t.Errorf("output lacks the seat here:\n%s", out)
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
		t.Fatal(err)
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
	takeSample = func(k capacity.Known) capacity.Sample { calls++; return old(k) }
	must(t, "capacity")
	if calls != 1 {
		t.Errorf("capacity sampled %d times, want 1", calls)
	}
}

func TestCapacityStartsNoProcess(t *testing.T) {
	isolate(t)
	must(t, "init")
	must(t, "project", "add")
	home := config.GlobalDir()
	before, kids := digest(t, home), children(t)
	must(t, "capacity")
	if after := children(t); !slices.Equal(after, kids) {
		t.Errorf("children %v after the run, want %v", after, kids)
	}
	if diff := sameDigest(before, digest(t, home)); len(diff) != 0 {
		t.Errorf("capacity wrote under the satelle home: %v", diff)
	}
}
