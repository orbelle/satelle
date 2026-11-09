//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// noTerminal stubs the controlling terminal away: no one can answer.
func noTerminal(t *testing.T) {
	t.Helper()
	old := openTerminal
	t.Cleanup(func() { openTerminal = old })
	openTerminal = func() (io.ReadWriteCloser, error) { return nil, errors.New("no terminal") }
}

// fakeTTY is a scripted controlling terminal: Read answers from in, Write
// records what was shown. With in nil, a read fails the test.
type fakeTTY struct {
	t     *testing.T
	in    io.Reader
	mu    sync.Mutex
	shown strings.Builder
	reads int
}

func (f *fakeTTY) Read(p []byte) (int, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	if f.in == nil {
		f.t.Errorf("the terminal was read")
		return 0, io.EOF
	}
	return f.in.Read(p)
}

func (f *fakeTTY) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shown.Write(p)
}

func (f *fakeTTY) Close() error { return nil }

func withTerminal(t *testing.T, answers string) *fakeTTY {
	t.Helper()
	tty := &fakeTTY{t: t}
	if answers != "" {
		tty.in = strings.NewReader(answers)
	}
	old := openTerminal
	t.Cleanup(func() { openTerminal = old })
	openTerminal = func() (io.ReadWriteCloser, error) { return tty, nil }
	return tty
}

// loggingSystemctl replaces the fixture's systemctl with one that logs every
// call and answers is-active from the fixture's timer-state file.
func loggingSystemctl(t *testing.T, f *doctorFixture) string {
	t.Helper()
	log := filepath.Join(f.bin, "systemctl.log")
	writeExec(t, filepath.Join(f.bin, "systemctl"), "#!/bin/sh\necho \"$*\" >> "+log+"\ncase \"$*\" in *is-active*) cat "+f.timerState+";; esac\n")
	return log
}

// systemctlCalls is the logged calls other than is-active and
// show-environment probes.
func systemctlCalls(t *testing.T, log string) []string {
	t.Helper()
	b, _ := os.ReadFile(log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" && !strings.Contains(l, "is-active") && !strings.Contains(l, "show-environment") {
			out = append(out, strings.TrimPrefix(l, "--user "))
		}
	}
	return out
}

// resolveJSON runs satelle doctor --resolve --json.
func resolveJSON(t *testing.T) (resolveReport, string, error) {
	t.Helper()
	out, err := run(t, "", "doctor", "--resolve", "--json")
	var rep resolveReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("doctor --resolve --json: %v\n%s", jerr, out)
	}
	return rep, out, err
}

// killedProc starts a child, kills and reaps it: a process that provably
// exited.
func killedProc(t *testing.T) procgroup.Proc {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	p := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return p
}

// chain reads id's events.
func chain(t *testing.T, id string) []ledger.Event {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ledger.OpenExisting(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func seatsOn(t *testing.T, id string) []item.Seat { return item.FoldSeats(chain(t, id)) }

// seatAs binds session on id at this tree from a kernel stamping machine and
// host: another machine's seat, or a legacy one recording none.
func seatAs(t *testing.T, id, session, machine, host string) {
	t.Helper()
	k, r, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Machine, k.Host = machine, host
	if _, err := k.Seat(id, session, kernel.LocalLocation(r.Tree), "", "test"); err != nil {
		t.Fatal(err)
	}
}

// --resolve runs the service install action when satelle.service or the sync
// timer is missing or the timer is not active, and the restart action when
// satelled does not answer or runs another build (sto_e127b2e6 AC1).
func TestResolveInstallsAndRestartsTheService(t *testing.T) {
	units := func(f *doctorFixture) string { return filepath.Join(f.home, ".config", "systemd", "user") }
	installCalls := []string{"daemon-reload", "enable " + serviceUnitName, "restart " + serviceUnitName, "enable --now " + syncTimerName}
	cases := []struct {
		name      string
		setup     func(t *testing.T, f *doctorFixture)
		want      []string // the issues' checks and problems, as substrings
		calls     []string
		writesAll bool
	}{
		{name: "unit files missing", setup: func(t *testing.T, f *doctorFixture) {
			for _, n := range []string{serviceUnitName, syncServiceName, syncTimerName} {
				_ = os.Remove(filepath.Join(units(f), n))
			}
		}, want: []string{serviceUnitName + " is not installed", syncTimerName + " is not installed"}, calls: installCalls, writesAll: true},
		{name: "timer inactive", setup: func(t *testing.T, f *doctorFixture) {
			writeFile(t, f.timerState, "inactive\n")
		}, want: []string{syncTimerName + " is inactive"}, calls: installCalls, writesAll: true},
		{name: "satelled not answering", setup: func(t *testing.T, f *doctorFixture) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			writeFile(t, filepath.Join(config.GlobalDir(), config.GlobalConfigName), fmt.Sprintf("[service]\nport = %d\n", port))
		}, want: []string{"not answering"}, calls: []string{"restart " + serviceUnitName}},
		{name: "running build differs", setup: func(t *testing.T, f *doctorFixture) {
			f.running.Version = "0.9.0"
		}, want: []string{"differs from the installed one"}, calls: []string{"restart " + serviceUnitName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDoctorFixture(t)
			noTerminal(t)
			log := loggingSystemctl(t, f)
			tc.setup(t, f)
			before, _, _ := doctorJSON(t)
			for _, w := range tc.want {
				if !slices.ContainsFunc(before.Issues, func(is doctorIssue) bool { return strings.Contains(is.Problem, w) && is.Resolve != "" }) {
					t.Fatalf("doctor reports no resolvable %q: %+v", w, before.Issues)
				}
			}
			_ = os.Remove(log)
			rep, out, err := resolveJSON(t)
			if err != nil {
				t.Fatalf("--resolve: %v\n%s", err, out)
			}
			if got := systemctlCalls(t, log); !slices.Equal(got, tc.calls) {
				t.Errorf("systemctl calls %q, want %q", got, tc.calls)
			}
			for _, w := range tc.want {
				if !slices.ContainsFunc(rep.Actions, func(a resolveAction) bool {
					return strings.Contains(a.Problem, w) && a.Result == resultApplied
				}) {
					t.Errorf("no %s action for %q in %+v", resultApplied, w, rep.Actions)
				}
			}
			if len(rep.Left) != 0 {
				t.Errorf("left %+v", rep.Left)
			}
			if tc.writesAll {
				for _, n := range []string{serviceUnitName, syncServiceName, syncTimerName} {
					if _, err := os.Stat(filepath.Join(units(f), n)); err != nil {
						t.Errorf("%s not written: %v", n, err)
					}
				}
			}
		})
	}
}

// A seat on this machine whose session's owner has exited is released without
// asking, with or without a terminal, and its record is pruned (AC2, AC8
// "orphan released").
func TestResolveReleasesAProvenOrphan(t *testing.T) {
	for _, withTTY := range []bool{false, true} {
		t.Run(map[bool]string{false: "no terminal", true: "terminal"}[withTTY], func(t *testing.T) {
			newDoctorFixture(t)
			id := createID(t, "story", "create", "--title", "orphan", "--criteria", "c")
			must(t, "story", "seat", id, "--session", "s-gone")
			dead := killedProc(t)
			home := config.GlobalDir()
			if err := seatlife.Own(home, "s-gone", dead); err != nil {
				t.Fatal(err)
			}
			var tty *fakeTTY
			if withTTY {
				tty = withTerminal(t, "")
			} else {
				noTerminal(t)
			}
			out, err := run(t, "", "doctor", "--resolve")
			if err != nil {
				t.Fatalf("--resolve: %v\n%s", err, out)
			}
			want := fmt.Sprintf("session gone: pid %d started %s has exited", dead.PID, dead.Start)
			if !strings.Contains(out, resultReleased+": seats: ") || !strings.Contains(out, want) {
				t.Errorf("output lacks the release and %q:\n%s", want, out)
			}
			evs := chain(t, id)
			last := evs[len(evs)-1]
			var rel item.SeatRelease
			if last.Kind != item.KindSeatReleased || last.Decode(&rel) != nil || rel.Session != "s-gone" {
				t.Errorf("chain ends in %s %s, want seat.released for s-gone", last.Kind, last.Payload)
			}
			if _, ok := seatlife.Load(home, "s-gone"); ok {
				t.Error("the gone session's record was not pruned")
			}
			if tty != nil && (tty.reads != 0 || tty.shown.Len() != 0) {
				t.Errorf("the terminal was used: %d reads, shown %q", tty.reads, tty.shown.String())
			}
		})
	}
}

// Every other orphan is shown with its evidence and released or kept on the
// answer at the terminal; without one it is only listed and --resolve exits 1
// (AC3, AC8 "orphan asked about").
func TestResolveAsksAboutOtherOrphans(t *testing.T) {
	rows := []struct {
		name    string
		seat    func(t *testing.T, id string)
		machine string
		session string
	}{
		{name: "this machine, session alive", seat: func(t *testing.T, id string) {
			must(t, "story", "seat", id, "--session", "s-live")
			if err := seatlife.Own(config.GlobalDir(), "s-live", procgroup.Self()); err != nil {
				t.Fatal(err)
			}
		}, machine: "machine: this machine", session: "session s-live: here (pid "},
		{name: "no machine recorded", seat: func(t *testing.T, id string) {
			seatAs(t, id, "s-live", "", "")
		}, machine: "machine: not recorded", session: "session s-live: unverified (no machine recorded)"},
	}
	outcomes := []struct {
		name, answer string // answer "" is no terminal
		released     bool
		exit         int
	}{
		{"release", "release\n", true, 0},
		{"keep", "keep\n", false, 0},
		{"no terminal", "", false, 1},
	}
	for _, row := range rows {
		for _, oc := range outcomes {
			t.Run(row.name+"/"+oc.name, func(t *testing.T) {
				f := newDoctorFixture(t)
				id := createID(t, "story", "create", "--title", "finished", "--criteria", "c")
				row.seat(t, id)
				must(t, "story", "set", id, "--status", "done")
				var tty *fakeTTY
				if oc.answer != "" {
					tty = withTerminal(t, oc.answer)
				} else {
					noTerminal(t)
				}
				rep, out, err := resolveJSON(t)
				if code := map[bool]int{true: 0, false: ExitCodeOf(err)}[err == nil]; code != oc.exit {
					t.Fatalf("exit %d, want %d: %v\n%s", ExitCodeOf(err), oc.exit, err, out)
				}
				released := len(seatsOn(t, id)) == 0
				if released != oc.released {
					t.Errorf("released %v, want %v", released, oc.released)
				}
				evidence := []string{"done, a terminal state", "local:" + f.dir, row.machine, row.session}
				var shown string
				switch {
				case tty != nil:
					shown = tty.shown.String()
					want := map[bool]string{true: resultReleased, false: resultKept}[oc.released]
					if len(rep.Actions) != 1 || rep.Actions[0].Result != want {
						t.Errorf("actions %+v, want one %s", rep.Actions, want)
					}
				default:
					if len(rep.Left) != 1 || !strings.Contains(rep.Left[0].Fix, "release "+id+" --session s-live") {
						t.Fatalf("left %+v, want the seat with its release command", rep.Left)
					}
					shown = rep.Left[0].Problem
				}
				for _, e := range evidence {
					if !strings.Contains(shown, e) {
						t.Errorf("evidence lacks %q:\n%s", e, shown)
					}
				}
			})
		}
	}
}

// A seat here whose session never settled an owner and whose hook processes
// have exited is unverified, not gone: doctor reports it as an issue to ask
// about, and --resolve never releases it unasked (sto_35c85724 AC1, AC3).
func TestResolveAsksAboutASessionWhoseHooksExited(t *testing.T) {
	for _, withTTY := range []bool{false, true} {
		t.Run(map[bool]string{false: "no terminal", true: "keep"}[withTTY], func(t *testing.T) {
			newDoctorFixture(t)
			id := createID(t, "story", "create", "--title", "hooks exited", "--criteria", "c")
			must(t, "story", "seat", id, "--session", "s-hooks")
			home := config.GlobalDir()
			a, b := killedProc(t), killedProc(t)
			if err := seatlife.Start(home, "s-hooks", []procgroup.Proc{a, b}); err != nil {
				t.Fatal(err)
			}
			rep, _, _ := doctorJSON(t)
			var seatIssues []doctorIssue
			for _, is := range rep.Issues {
				if is.Check == "seats" {
					seatIssues = append(seatIssues, is)
				}
			}
			if len(seatIssues) != 1 || seatIssues[0].Resolve != resolveAsk || !strings.Contains(seatIssues[0].Problem, id) {
				t.Fatalf("seat issues %+v, want one %s for %s", seatIssues, resolveAsk, id)
			}
			for _, want := range []string{"session unverified", "never settled", fmt.Sprint(a.PID), fmt.Sprint(b.PID)} {
				if !strings.Contains(seatIssues[0].Problem, want) {
					t.Errorf("problem lacks %q: %s", want, seatIssues[0].Problem)
				}
			}
			var note string
			for _, n := range rep.Notes {
				if strings.HasPrefix(n, "seats: ") {
					note = n
				}
			}
			if !strings.Contains(note, "unverified: "+id+" s-hooks") || strings.Contains(note, "gone: ") {
				t.Errorf("seats note %q, want %s unverified and nothing gone", note, id)
			}
			if withTTY {
				withTerminal(t, "keep\n")
			} else {
				noTerminal(t)
			}
			res, out, err := resolveJSON(t)
			if withTTY {
				if err != nil {
					t.Fatalf("--resolve: %v\n%s", err, out)
				}
				if len(res.Actions) != 1 || res.Actions[0].Result != resultKept {
					t.Errorf("actions %+v, want one %s", res.Actions, resultKept)
				}
			} else {
				if ExitCodeOf(err) != 1 {
					t.Fatalf("exit %d, want 1: %v\n%s", ExitCodeOf(err), err, out)
				}
				if len(res.Left) != 1 || !strings.Contains(res.Left[0].Fix, "release "+id+" --session s-hooks") {
					t.Fatalf("left %+v, want the seat with its release command", res.Left)
				}
			}
			if s := seatsOn(t, id); len(s) != 1 {
				t.Errorf("the seat was released: %+v", s)
			}
			if _, ok := seatlife.Load(home, "s-hooks"); !ok {
				t.Error("the seated session's record was pruned")
			}
		})
	}
}

// fourSeats seats four open stories: here (this machine, owner alive),
// elsewhere (another machine), gone (owner killed) and unverified (no machine
// recorded). It returns their ids by class.
func fourSeats(t *testing.T) map[seatlife.Standing]string {
	t.Helper()
	ids := map[seatlife.Standing]string{}
	home := config.GlobalDir()
	for _, st := range []seatlife.Standing{seatlife.Here, seatlife.Elsewhere, seatlife.Gone, seatlife.Unverified} {
		ids[st] = createID(t, "story", "create", "--title", "seat "+string(st), "--criteria", "c")
	}
	must(t, "story", "seat", ids[seatlife.Here], "--session", "s-here")
	if err := seatlife.Own(home, "s-here", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	seatAs(t, ids[seatlife.Elsewhere], "s-elsewhere", "m-other", "other-host")
	must(t, "story", "seat", ids[seatlife.Gone], "--session", "s-gone")
	if err := seatlife.Own(home, "s-gone", killedProc(t)); err != nil {
		t.Fatal(err)
	}
	seatAs(t, ids[seatlife.Unverified], "s-unverified", "", "")
	return ids
}

// doctor classes every seat: one engaged here, the other machine's seat
// elsewhere, the killed owner's gone, the legacy one unverified; only the
// gone one is an issue, and --resolve leaves the elsewhere seat bound (AC6,
// AC8 "remote-machine seat shown as elsewhere").
func TestDoctorClassesEverySeat(t *testing.T) {
	newDoctorFixture(t)
	noTerminal(t)
	ids := fourSeats(t)
	rep, _, _ := doctorJSON(t)
	var note string
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, "seats: ") {
			note = n
		}
	}
	for _, want := range []string{
		"seats: 1 engaged here",
		"elsewhere: " + ids[seatlife.Elsewhere] + " s-elsewhere on other-host",
		"gone: " + ids[seatlife.Gone] + " s-gone (pid ",
		"unverified: " + ids[seatlife.Unverified] + " s-unverified (no machine recorded)",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("seats note lacks %q:\n%s", want, note)
		}
	}
	if len(rep.Issues) != 1 || rep.Issues[0].Resolve != resolveReleaseSeat || !strings.Contains(rep.Issues[0].Problem, ids[seatlife.Gone]) {
		t.Fatalf("issues %+v, want one %s for the gone seat", rep.Issues, resolveReleaseSeat)
	}
	if _, out, err := resolveJSON(t); err != nil {
		t.Fatalf("--resolve: %v\n%s", err, out)
	}
	if s := seatsOn(t, ids[seatlife.Elsewhere]); len(s) != 1 {
		t.Errorf("the elsewhere seat was released: %+v", s)
	}
	if s := seatsOn(t, ids[seatlife.Gone]); len(s) != 0 {
		t.Errorf("the gone seat is still bound: %+v", s)
	}
}

// --resolve opens one item listing every init and configured-check finding,
// opens no second one while it is open, honours doctor.toml [resolve], and
// with an invalid configuration records nothing; it never writes the tree or
// .git (AC7, AC8 "one fix item for several findings").
func TestResolveOpensOneFixItemForEveryFinding(t *testing.T) {
	cases := []struct {
		name, resolve string
		category, tag string
		invalid       bool
	}{
		{name: "no [resolve]", tag: kernel.DefaultResolveTag},
		{name: "[resolve]", resolve: "\n[resolve]\ncategory = \"chore\"\ntag = \"fix-me\"\n", category: "chore", tag: "fix-me"},
		{name: "invalid configuration", invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDoctorFixture(t)
			noTerminal(t)
			if tc.resolve != "" {
				p := filepath.Join(f.dir, ".satelle", "doctor.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+tc.resolve)
			}
			before0 := itemIDs(t)
			if err := os.Remove(filepath.Join(f.dir, ".satelle", "agents.toml")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.dir, "FAIL"), "skills/a.md:1: first finding\nskills/b.md:2: second finding\n")
			if tc.invalid {
				p := filepath.Join(f.dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\nbogus_key = 1\n")
			}
			before := digest(t, f.dir)
			rep, out, err := resolveJSON(t)
			findings := []string{"init: satelle init would add .satelle/agents.toml", "check:fixture-check: skills/a.md:1: first finding", "check:fixture-check: skills/b.md:2: second finding"}
			if tc.invalid {
				if ExitCodeOf(err) != 1 {
					t.Fatalf("exit %d, want 1\n%s", ExitCodeOf(err), out)
				}
				if got := itemIDs(t); !slices.Equal(got, before0) {
					t.Fatalf("an item was recorded under an invalid configuration: %v", got)
				}
				if len(rep.Left) == 0 || rep.Left[0].Check != "config" || !strings.Contains(rep.Left[0].Problem, "bogus_key") {
					t.Fatalf("the config finding is not first: %+v", rep.Left)
				}
				for _, w := range findings {
					if !slices.ContainsFunc(rep.Left, func(l resolveLeft) bool {
						return l.Check+": "+l.Problem == w && strings.HasPrefix(l.Fix, configFirst)
					}) {
						t.Errorf("left lacks %q prefixed %q: %+v", w, configFirst, rep.Left)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("--resolve: %v\n%s", err, out)
				}
				fixes := newItems(t, before0)
				if len(fixes) != 1 || rep.Item != fixes[0].ID {
					t.Fatalf("items opened %+v, report item %q", fixes, rep.Item)
				}
				it := fixes[0]
				if it.Kind != kernel.DefaultResolveKind || !slices.Contains(it.Tags, tc.tag) || it.Category != tc.category {
					t.Errorf("item kind %q tags %v category %q, want story, %q, %q", it.Kind, it.Tags, it.Category, tc.tag, tc.category)
				}
				for _, w := range findings {
					if !strings.Contains(it.Body, "- "+w+" — fix: ") {
						t.Errorf("body lacks %q:\n%s", w, it.Body)
					}
				}
				if len(it.Criteria) != len(findings) {
					t.Errorf("criteria %v, want %d", it.Criteria, len(findings))
				}
				text, err := run(t, "", "doctor", "--resolve")
				if err != nil {
					t.Fatalf("second --resolve: %v\n%s", err, text)
				}
				if !strings.Contains(text, "carried by "+it.ID) {
					t.Errorf("second run does not name the open item:\n%s", text)
				}
				if again := newItems(t, before0); len(again) != 1 {
					t.Errorf("a second item was opened: %+v", again)
				}
			}
			if d := sameDigest(before, digest(t, f.dir)); len(d) != 0 {
				t.Fatalf("--resolve wrote the tree: %v", d)
			}
		})
	}
}

// itemIDs lists the repo's items, read-only.
func itemIDs(t *testing.T) []string {
	t.Helper()
	items, err := readItems(loadDoctorInput(""))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ri := range items {
		out = append(out, ri.it.ID)
	}
	slices.Sort(out)
	return out
}

// newItems is the items not in before.
func newItems(t *testing.T, before []string) []item.Item {
	t.Helper()
	items, err := readItems(loadDoctorInput(""))
	if err != nil {
		t.Fatal(err)
	}
	var out []item.Item
	for _, ri := range items {
		if !slices.Contains(before, ri.it.ID) {
			out = append(out, ri.it)
		}
	}
	return out
}

// A substrate finding (a skill naming a command this binary lacks) is a
// fix-item issue, and --resolve opens the one fix item listing it beside the
// other substrate findings; the tree and .git are unchanged (AC7).
func TestResolvePutsSubstrateFindingsOnTheFixItem(t *testing.T) {
	f := newDoctorFixture(t)
	noTerminal(t)
	writeFile(t, filepath.Join(f.dir, ".satelle", "skills", "stale.md"), "---\nname: stale\ntype: skill\ndescription: Names what the binary lacks.\n---\n\nRun `satelle nosuchcmd <id>`.\n")
	if err := os.Remove(filepath.Join(f.dir, ".satelle", "agents.toml")); err != nil {
		t.Fatal(err)
	}
	doc, _, _ := doctorJSON(t)
	var sub []doctorIssue
	for _, is := range doc.Issues {
		if strings.Contains(is.Problem, "satelle nosuchcmd") {
			sub = append(sub, is)
		}
	}
	if len(sub) != 1 || sub[0].Resolve != resolveFixItem {
		t.Fatalf("substrate issues %+v, want one %s for satelle nosuchcmd (all: %+v)", sub, resolveFixItem, doc.Issues)
	}
	before0 := itemIDs(t)
	before := digest(t, f.dir)
	rep, out, err := resolveJSON(t)
	if err != nil {
		t.Fatalf("--resolve: %v\n%s", err, out)
	}
	fixes := newItems(t, before0)
	if len(fixes) != 1 || rep.Item != fixes[0].ID {
		t.Fatalf("items opened %+v, report item %q", fixes, rep.Item)
	}
	for _, w := range []string{sub[0].Check + ": " + sub[0].Problem + " — fix: ", "init: satelle init would add .satelle/agents.toml — fix: "} {
		if !strings.Contains(fixes[0].Body, "- "+w) {
			t.Errorf("fix item body lacks %q:\n%s", w, fixes[0].Body)
		}
	}
	if !slices.ContainsFunc(rep.Actions, func(a resolveAction) bool {
		return a.Check == sub[0].Check && a.Result == resultOpened && a.Detail == fixes[0].ID
	}) {
		t.Errorf("no %s action for the substrate finding: %+v", resultOpened, rep.Actions)
	}
	if d := sameDigest(before, digest(t, f.dir)); len(d) != 0 {
		t.Fatalf("--resolve wrote the tree: %v", d)
	}
}
