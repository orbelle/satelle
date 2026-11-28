//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/capacity"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode"
)

func commandRemote(exec string) string {
	return `version = 1
provider = "command"
satelle_version = "1.0.0"

[drive]
kind = "item"

[connect]
start = ["true"]
network = ["true"]
copy_in = ["true", "{src}", "{dst}"]
exec = ` + exec + `
stop = ["true"]
`
}

func writeRemote(t *testing.T, dir, id, body string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".satelle", kernel.RemotesDir, id, kernel.RemoteFile), body)
}

func boxReport() capacityReport {
	return capacityReport{
		Host: "box-host", MachineID: "box-id", Cores: 8,
		MemTotal: 16 << 30, MemAvailable: 9 << 30, Load: [3]float64{0.5, 0.25, 0.125},
		Agents: map[string]int{"claude": 2, "pi": 0},
		Seats:  []engagedSeat{{Item: "sto_box", Session: "s-box", Location: kernel.LocationLocal + "/srv/box"}},
	}
}

func fixedSample(t *testing.T) {
	t.Helper()
	old := readMachine
	t.Cleanup(func() { readMachine = old })
	readMachine = func(func() ([]procgroup.Entry, error)) capacity.Snapshot {
		return capacity.Snapshot{
			TakenAt: time.Now(), Cores: 4, MemTotal: 8 << 30, MemAvailable: 3 << 30, Load: [3]float64{1, 2, 3},
			Procs: []capacity.Proc{{PID: 10, PPID: 1, Names: []string{"claude"}}},
			Errs:  map[string]string{capacity.FigLoad: "unavailable on plan9"},
		}
	}
}

type remoteFixture struct {
	dir, marker, script string
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := &remoteFixture{dir: newDoctorFixture(t).dir}
	fixedSample(t)
	tmp := t.TempDir()
	f.marker = filepath.Join(tmp, "asked")
	f.script = filepath.Join(tmp, "box.sh")
	b, err := json.Marshal(boxReport())
	if err != nil {
		t.Fatal(err)
	}
	f.setBox(t, "echo 'Welcome to box' >&2\necho '"+string(b)+"'\n")
	writeRemote(t, f.dir, "box", commandRemote(`["sh", "`+f.script+`"]`))
	writeRemote(t, f.dir, "down", commandRemote(`["sh", "-c", "echo 'ssh: connect to host down: Connection refused' >&2; exit 255"]`))
	writeRemote(t, f.dir, "pernode", commandRemote(`["sh", "-c", "exit 1", "{node}"]`))
	return f
}

func (f *remoteFixture) setBox(t *testing.T, body string) {
	t.Helper()
	writeFile(t, f.script, "touch "+f.marker+"\n"+body)
}

func blockFrom(out, header string) (string, bool) {
	i := strings.Index(out, header)
	if i < 0 {
		return "", false
	}
	rest := out[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j+1]
	}
	return rest, true
}

func TestCapacityReportsAReachedMachine(t *testing.T) {
	newRemoteFixture(t)
	out := must(t, "capacity")

	here := must(t, "capacity", "--here")
	if !strings.HasPrefix(out, here+"\n") {
		t.Errorf("this machine's block is not first and unchanged:\n%s\nwant prefix:\n%s", out, here)
	}
	block, ok := blockFrom(out, "machine   box-host (box-id) via remote box\n")
	if !ok {
		t.Fatalf("no block for the reached machine box:\n%s", out)
	}
	var want bytes.Buffer
	printCapacity(&want, boxReport(), "box")
	if block != want.String() {
		t.Errorf("box block:\n%s\nwant the same lines as this machine's printer:\n%s", block, want.String())
	}
	if !strings.Contains(out, "machine   remote down: unreachable: ") || !strings.Contains(out, "Connection refused") {
		t.Errorf("down is not named unreachable with its reason:\n%s", out)
	}
	if strings.Contains(out, "pernode") {
		t.Errorf("a per-run {node} remote was reached:\n%s", out)
	}
	if strings.Index(out, "remote box") > strings.Index(out, "remote down") {
		t.Errorf("remotes are not in sorted order:\n%s", out)
	}
}

func TestCapacityUnreachableDoesNotFailOthers(t *testing.T) {
	f := newRemoteFixture(t)
	writeRemote(t, f.dir, "aaa-down", commandRemote(`["sh", "-c", "exit 255"]`))
	out := must(t, "capacity")
	down := strings.Index(out, "machine   remote aaa-down: unreachable:")
	box := strings.Index(out, "machine   box-host (box-id) via remote box")
	if down < 0 || box < 0 || down > box {
		t.Fatalf("want aaa-down unreachable before the complete box block:\n%s", out)
	}
	block, _ := blockFrom(out, "machine   box-host (box-id) via remote box\n")
	var want bytes.Buffer
	printCapacity(&want, boxReport(), "box")
	if block != want.String() {
		t.Errorf("box block after an unreachable machine:\n%s\nwant:\n%s", block, want.String())
	}

	old := remoteTimeout
	t.Cleanup(func() { remoteTimeout = old })
	remoteTimeout = 200 * time.Millisecond
	f.setBox(t, "exec sleep 5\n")
	start := time.Now()
	out = must(t, "capacity")
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("a hung machine held the report for %s", took)
	}
	line := capacityLine(t, out, "machine   remote box:")
	if !strings.HasPrefix(line, "unreachable: ") || !strings.Contains(line, "deadline exceeded") {
		t.Errorf("hung box line %q, want unreachable: ... deadline exceeded:\n%s", line, out)
	}
	if !strings.HasPrefix(out, must(t, "capacity", "--here")) {
		t.Errorf("this machine's block is not intact:\n%s", out)
	}
}

func TestCapacityHereReachesNoRemote(t *testing.T) {
	f := newRemoteFixture(t)
	out := must(t, "capacity", "--here")
	if _, err := os.Stat(f.marker); err == nil {
		t.Errorf("--here ran the box remote:\n%s", out)
	}
	if strings.Count(out, "machine   ") != 1 || strings.Contains(out, "remote") {
		t.Errorf("--here printed more than this machine:\n%s", out)
	}
	must(t, "capacity")
	if _, err := os.Stat(f.marker); err != nil {
		t.Errorf("without --here the box remote was not asked: %v", err)
	}
}

func TestCapacityJSONRoundTrip(t *testing.T) {
	newRemoteFixture(t)
	fourSeats(t)
	must(t, "project", "add")
	var rep capacityReport
	if err := json.Unmarshal([]byte(must(t, "capacity", "--json", "--here")), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Seats) != 1 || rep.Unavailable[capacity.FigLoad] == "" {
		t.Errorf("decoded %+v, want the one seat here and the unavailable load", rep)
	}
	var got bytes.Buffer
	printCapacity(&got, rep, "")
	if text := must(t, "capacity", "--here"); got.String() != text {
		t.Errorf("printed from the JSON report:\n%s\nwant the text report:\n%s", got.String(), text)
	}

	var all struct {
		Machines []machineJSON `json:"machines"`
	}
	if err := json.Unmarshal([]byte(must(t, "capacity", "--json")), &all); err != nil {
		t.Fatal(err)
	}
	byRemote := map[string]machineJSON{}
	for _, m := range all.Machines {
		byRemote[m.Remote] = m
	}
	if m := byRemote[""]; m.Report == nil {
		t.Errorf("no report for this machine: %+v", all)
	}
	if m := byRemote["box"]; m.Report == nil || m.Report.Host != "box-host" {
		t.Errorf("box %+v, want its report", m)
	}
	if m := byRemote["down"]; m.Report != nil || !strings.Contains(m.Unreachable, "Connection refused") {
		t.Errorf("down %+v, want unreachable", m)
	}
	if _, ok := byRemote["pernode"]; ok {
		t.Errorf("pernode was reached: %+v", all)
	}
}

func TestRemoteIDOf(t *testing.T) {
	f := newRemoteFixture(t)
	if id, err := remoteIDOf("remotes/box/remote.toml"); err != nil || id != "box" {
		t.Errorf("remoteIDOf = %q, %v; want box", id, err)
	}
	dataDir := filepath.Join(f.dir, ".satelle")
	rels := kernel.RemoteFiles(dataDir)
	if len(rels) != 3 {
		t.Fatalf("RemoteFiles = %v, want three", rels)
	}
	for _, rel := range rels {
		id, err := remoteIDOf(rel)
		if err != nil {
			t.Errorf("remoteIDOf(%q): %v", rel, err)
			continue
		}
		if _, _, err := remotenode.LoadLocal(dataDir, id); err != nil {
			t.Errorf("LoadLocal(%q) from %q: %v", id, rel, err)
		}
	}
	for _, bad := range []string{"remote.toml", "remotes/remote.toml", "x/box/remote.toml", "remotes/box/other.toml"} {
		if id, err := remoteIDOf(bad); err == nil {
			t.Errorf("remoteIDOf(%q) = %q, want an error", bad, id)
		}
	}
	if _, err := remoteIDOf("remotes/../remote.toml"); err == nil {
		t.Errorf("remoteIDOf accepted a path outside remotes/")
	}
	if _, _, err := remotenode.LoadLocal(dataDir, ".."); err == nil || !strings.Contains(err.Error(), "is not a remote id") {
		t.Errorf("LoadLocal(..) = %v, want the id refused", err)
	}
}

func TestStandingMachineRule(t *testing.T) {
	for _, c := range []struct {
		name string
		def  remotedef.Definition
		want bool
	}{
		{"command, fixed exec", remotedef.Definition{Provider: remotedef.ProviderCommand, Connect: remotedef.Connect{Exec: []string{"ssh", "agents-box", "--"}}}, true},
		{"command, node arg", remotedef.Definition{Provider: remotedef.ProviderCommand, Connect: remotedef.Connect{Exec: []string{"node-cli", "exec", "{node}", "--"}}}, false},
		{"command, node in host", remotedef.Definition{Provider: remotedef.ProviderCommand, Connect: remotedef.Connect{Exec: []string{"ssh", "w-{node}.box"}}}, false},
		{"command, no exec", remotedef.Definition{Provider: remotedef.ProviderCommand}, false},
		{"docker-local", remotedef.Definition{Provider: remotedef.ProviderDockerLocal}, false},
		{"fly-sprite", remotedef.Definition{Provider: remotedef.ProviderFlySprite}, false},
	} {
		if got := standingMachine(c.def); got != c.want {
			t.Errorf("%s: standingMachine = %v, want %v", c.name, got, c.want)
		}
	}
}
