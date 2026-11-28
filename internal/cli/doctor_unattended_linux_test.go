//go:build linux

package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

type doctorLogLine struct {
	result, project, issue, detail string
}

func doctorLog(t *testing.T) []doctorLogLine {
	t.Helper()
	out := must(t, "logs", "--doctor")
	var lines []doctorLogLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" || strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "no doctor actions logged") {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 4 {
			t.Fatalf("doctor log line %q has %d fields, want at least 4", l, len(f))
		}
		if _, err := time.Parse(time.RFC3339, f[0]); err != nil {
			t.Errorf("doctor log line %q does not start with its time: %v", l, err)
		}
		dl := doctorLogLine{result: f[1], project: f[2], issue: f[3]}
		if len(f) > 4 {
			dl.detail = f[4]
		}
		lines = append(lines, dl)
	}
	return lines
}

func endsReleased(evs []ledger.Event, session string) bool {
	if len(evs) == 0 {
		return false
	}
	last := evs[len(evs)-1]
	var rel item.SeatRelease
	return last.Kind == item.KindSeatReleased && last.Decode(&rel) == nil && rel.Session == session && last.Actor == syncServiceName
}

func holdsRelease(evs []ledger.Event) bool {
	return slices.ContainsFunc(evs, func(e ledger.Event) bool { return e.Kind == item.KindSeatReleased })
}

func hashes(evs []ledger.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Hash)
	}
	return out
}

func storeHashes(srv *fakeserver.Server, id string) []string {
	var out []string
	for _, e := range srv.Events("shared", id) {
		out = append(out, e.GetHash())
	}
	return out
}

func storeEndsReleased(srv *fakeserver.Server, id string) bool {
	evs := srv.Events("shared", id)
	return len(evs) > 0 && evs[len(evs)-1].GetKind() == item.KindSeatReleased && evs[len(evs)-1].GetActor() == syncServiceName
}

func leftSeat(u syncstate.Unattended, id, session string) (syncstate.UnattendedIssue, bool) {
	for _, l := range u.Left {
		if l.Check == "seats" && l.Key == id+" "+session {
			return l, true
		}
	}
	return syncstate.UnattendedIssue{}, false
}

func recordOf(t *testing.T, dir string) syncstate.Unattended {
	t.Helper()
	r, err := findRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := syncstate.Load(config.GlobalDir(), uiRepoRoot(r))
	if err != nil {
		t.Fatal(err)
	}
	return st.Unattended
}

func ownedSeat(t *testing.T, id, session string, p procgroup.Proc) {
	t.Helper()
	must(t, "story", "seat", id, "--session", session)
	if err := seatlife.Own(config.GlobalDir(), session, p); err != nil {
		t.Fatal(err)
	}
}

func ownedStaleSeat(t *testing.T, id, session string, p procgroup.Proc) {
	t.Helper()
	machine, host := thisMachine(t)
	staleSeat(t, id, session, machine, host)
	if err := seatlife.Own(config.GlobalDir(), session, p); err != nil {
		t.Fatal(err)
	}
}

func endsReleasedAny(evs []ledger.Event, session string) bool {
	if len(evs) == 0 {
		return false
	}
	last := evs[len(evs)-1]
	var rel item.SeatRelease
	return last.Kind == item.KindSeatReleased && last.Decode(&rel) == nil && rel.Session == session
}

func otherRepo(t *testing.T, back string) string {
	t.Helper()
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, other, "init", "-q")
	gittest.Git(t, other, "commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(other)
	must(t, "init")
	t.Chdir(back)
	return other
}

func registered(t *testing.T) string {
	t.Helper()
	must(t, "project", "add")
	gc, err := config.LoadGlobal()
	if err != nil || len(gc.Projects.Paths) != 1 {
		t.Fatalf("setup: [projects] paths %v, %v; want this repo", gc.Projects.Paths, err)
	}
	return gc.Projects.Paths[0]
}

func sessionLists(t *testing.T, dir, check string) (inCtx, inMsg bool) {
	t.Helper()
	ctx, msg, _ := hookContextText(t, dir)
	return strings.Contains(ctx, "  "+check+" ("), strings.Contains(msg, "  "+check+" (")
}

func wantListed(t *testing.T, dir, check string, want bool) {
	t.Helper()
	inCtx, inMsg := sessionLists(t, dir, check)
	if inCtx != want || inMsg != want {
		ctx, msg, _ := hookContextText(t, dir)
		t.Errorf("session start lists %s: context %v, notice %v; want %v\ncontext:\n%s\nnotice:\n%s", check, inCtx, inMsg, want, ctx, msg)
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestAutosyncReleasesStaleSeats(t *testing.T) {
	newDoctorFixture(t)
	project := registered(t)
	home := config.GlobalDir()

	type row struct {
		id, session string
		released    bool
	}
	terminal := func(title, session string, seat func(id string)) row {
		id := createID(t, "story", "create", "--title", title, "--criteria", "c")
		must(t, "story", "set", id, "--status", "done")
		seat(id)
		return row{id, session, true}
	}
	rows := []row{
		terminal("here, owner alive", "s-here", func(id string) { ownedStaleSeat(t, id, "s-here", procgroup.Self()) }),
		terminal("elsewhere", "s-other", func(id string) { staleSeat(t, id, "s-other", "m-other", "other-host") }),
		terminal("no machine", "s-legacy", func(id string) { staleSeat(t, id, "s-legacy", "", "") }),
	}
	gone := createID(t, "story", "create", "--title", "gone session", "--criteria", "c")
	ownedSeat(t, gone, "s-gone", killedProc(t))
	rows = append(rows, row{gone, "s-gone", true})
	ask := createID(t, "story", "create", "--title", "worktree gone", "--criteria", "c")
	_, location := missingWorktree(t)
	seatAt(t, ask, "s-ask", "", "", location)
	rows = append(rows, row{ask, "s-ask", false})
	live := createID(t, "story", "create", "--title", "live", "--criteria", "c")
	ownedSeat(t, live, "s-live", procgroup.Self())
	rows = append(rows, row{live, "s-live", false})
	if _, ok := seatlife.Load(home, "s-live"); !ok {
		t.Fatal("setup: no owner record for s-live")
	}

	must(t, "ledger", "autosync")

	for _, r := range rows {
		evs := chain(t, r.id)
		if r.released {
			if !endsReleased(evs, r.session) {
				last := evs[len(evs)-1]
				t.Errorf("%s: chain ends in %s by %s %s, want seat.released for %s by %s", r.id, last.Kind, last.Actor, last.Payload, r.session, syncServiceName)
			}
			continue
		}
		if s := seatsOn(t, r.id); len(s) != 1 || s[0].Session != r.session {
			t.Errorf("%s: seats %+v, want %s still bound", r.id, s, r.session)
		}
	}
	u := syncStateOf(t).Unattended
	l, ok := leftSeat(u, ask, "s-ask")
	if !ok || l.Why != leftNoTTY || !strings.Contains(l.Fix, "release "+ask+" --session s-ask") {
		t.Errorf("record left %+v, want %s s-ask with %q and its release command", u.Left, ask, leftNoTTY)
	}
	if _, ok := leftSeat(u, live, "s-live"); ok {
		t.Errorf("record left %+v names the live seat", u.Left)
	}
	lines := doctorLog(t)
	if len(lines) != 4 {
		t.Fatalf("doctor log %+v, want 4 lines", lines)
	}
	for _, r := range rows[:4] {
		if !slices.ContainsFunc(lines, func(l doctorLogLine) bool {
			return l.result == resultReleased && l.project == project && strings.HasPrefix(l.issue, "seats: "+r.id+" is seated by session "+r.session+" ")
		}) {
			t.Errorf("doctor log %+v has no released line on %s naming %s and %s", lines, project, r.id, r.session)
		}
	}
	for _, l := range lines {
		if strings.Contains(l.issue, ask) || strings.Contains(l.issue, live) {
			t.Errorf("log line %+v names a seat the pass kept", l)
		}
	}

	lengths := map[string]int{}
	for _, r := range rows {
		lengths[r.id] = len(chain(t, r.id))
	}
	must(t, "ledger", "autosync")
	for _, r := range rows {
		if n := len(chain(t, r.id)); n != lengths[r.id] {
			t.Errorf("%s: the second pass appended %d event(s)", r.id, n-lengths[r.id])
		}
	}
	if again := doctorLog(t); len(again) != len(lines) {
		t.Errorf("the second pass wrote %d log line(s): %+v", len(again)-len(lines), again)
	}
}

func TestAutosyncReleaseDoesNotFork(t *testing.T) {
	t.Run("released where it was closed", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		a, b := boundPair(t, srv)
		a.use(t)
		id := createID(t, "story", "create", "--title", "closed on a", "--criteria", "c")
		must(t, "story", "set", id, "--status", "done")
		machine, host := thisMachine(t)
		staleSeat(t, id, "s-a", machine, host)
		autosync(t, a)
		if !storeEndsReleased(srv, id) {
			t.Fatalf("the store's chain of %s does not end in a release by %s", id, syncServiceName)
		}
		if st := syncStateOf(t); st.SyncPushed != 1 || st.SyncReason != "" {
			t.Errorf("A's sync record %+v, want 1 pushed and no reason", st)
		}
		onA := chain(t, id)
		autosync(t, b)
		onB := chain(t, id)
		if !slices.Equal(hashes(onA), hashes(onB)) || !slices.Equal(hashes(onA), storeHashes(srv, id)) {
			t.Errorf("chains differ: A %d, B %d, store %d events", len(onA), len(onB), len(srv.Events("shared", id)))
		}
		if st := syncStateOf(t); len(st.SyncForked) != 0 || st.SyncReason != "" {
			t.Errorf("B's sync record %+v, want nothing forked and no reason", st)
		}
	})
	t.Run("released where it was moved", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		a, b := boundPair(t, srv)
		a.use(t)
		id := createID(t, "story", "create", "--title", "seated on a", "--criteria", "c")
		must(t, "story", "seat", id, "--session", "s-a")
		autosync(t, a)
		autosync(t, b)
		runOn(t, b, "story", "set", id, "--status", "done")
		if !endsReleasedAny(chain(t, id), "s-a") {
			t.Fatalf("B's move to done did not release A's seat")
		}
		autosync(t, b)
		onB := chain(t, id)
		if evs := srv.Events("shared", id); len(evs) == 0 || evs[len(evs)-1].GetKind() != item.KindSeatReleased {
			t.Fatalf("B did not push the release of A's seat")
		}
		autosync(t, a)
		onA := chain(t, id)
		if !slices.Equal(hashes(onA), hashes(onB)) || !slices.Equal(hashes(onA), storeHashes(srv, id)) {
			t.Errorf("chains differ: A %d, B %d, store %d events", len(onA), len(onB), len(srv.Events("shared", id)))
		}
		if st := syncStateOf(t); len(st.SyncForked) != 0 || st.SyncReason != "" {
			t.Errorf("A's sync record %+v, want nothing forked and no reason", st)
		}
	})
}

func TestAutosyncLeavesSeatsWhenTheStoreIsNotReached(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	other := otherRepo(t, m.dir)
	must(t, "project", "add", m.dir)
	must(t, "project", "add", other)
	home := config.GlobalDir()

	done := createID(t, "story", "create", "--title", "to close", "--criteria", "c")
	gone := createID(t, "story", "create", "--title", "to lose its session", "--criteria", "c")
	ownedSeat(t, gone, "s-gone", procgroup.Self())
	must(t, "ledger", "autosync")
	if len(seatsOn(t, gone)) != 1 {
		t.Fatal("setup: a live seat was released")
	}

	srv.SetDown(true)
	must(t, "story", "set", done, "--status", "done")
	ownedStaleSeat(t, done, "s-done", procgroup.Self())
	if err := seatlife.Own(home, "s-gone", killedProc(t)); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	local := createID(t, "story", "create", "--title", "local store", "--criteria", "c")
	must(t, "story", "set", local, "--status", "done")
	machine, host := thisMachine(t)
	staleSeat(t, local, "s-local", machine, host)
	t.Chdir(m.dir)

	if out, err := run(t, "", "ledger", "autosync"); err == nil {
		t.Fatalf("autosync with the store down exited 0:\n%s", out)
	}
	for _, s := range []struct{ id, session string }{{done, "s-done"}, {gone, "s-gone"}} {
		if len(seatsOn(t, s.id)) != 1 || holdsRelease(chain(t, s.id)) {
			t.Errorf("%s: released with the store down", s.id)
		}
		l, ok := leftSeat(recordOf(t, m.dir), s.id, s.session)
		if !ok || !strings.HasPrefix(l.Why, leftNotSynced) || !strings.Contains(l.Fix, "release "+s.id+" --session "+s.session) {
			t.Errorf("%s: left %+v, want it with %q and its release command", s.id, l, leftNotSynced)
		}
	}
	wantListed(t, m.dir, "seats", true)
	t.Chdir(other)
	if !endsReleased(chain(t, local), "s-local") {
		t.Errorf("the local-store project's seat was not released in the same pass")
	}
	t.Chdir(m.dir)
	lines := doctorLog(t)
	if len(lines) != 1 || lines[0].result != resultReleased || lines[0].project != other || !strings.Contains(lines[0].issue, local) {
		t.Errorf("doctor log %+v, want one released line for %s in %s", lines, local, other)
	}

	srv.SetDown(false)
	must(t, "ledger", "autosync")
	for _, s := range []struct{ id, session string }{{done, "s-done"}, {gone, "s-gone"}} {
		if !endsReleased(chain(t, s.id), s.session) || !storeEndsReleased(srv, s.id) {
			t.Errorf("%s: not released and pushed once the store answered", s.id)
		}
		if _, ok := leftSeat(recordOf(t, m.dir), s.id, s.session); ok {
			t.Errorf("%s: still left after the release", s.id)
		}
	}
}

func TestAutosyncLeavesSeatsOnUnsyncedItems(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a, b := boundPair(t, srv)
	a.use(t)
	x := createID(t, "story", "create", "--title", "forks", "--criteria", "c")
	autosync(t, a)
	autosync(t, b)
	runOn(t, a, "story", "set", x, "--title", "edited on a")
	runOn(t, b, "story", "set", x, "--status", "done")
	machine, host := thisMachine(t)
	staleSeat(t, x, "s-x", machine, host)
	autosync(t, a)

	b.use(t)
	y := createID(t, "story", "create", "--title", "in sync", "--criteria", "c")
	must(t, "story", "set", y, "--status", "done")
	staleSeat(t, y, "s-y", machine, host)
	if out, err := run(t, "", "ledger", "autosync"); err == nil {
		t.Fatalf("autosync with a forked item exited 0:\n%s", out)
	}
	if s := seatsOn(t, x); len(s) != 1 {
		t.Errorf("the forked item's seat was released: %+v", s)
	}
	if l, ok := leftSeat(syncStateOf(t).Unattended, x, "s-x"); !ok || !strings.HasPrefix(l.Why, leftNotSynced) {
		t.Errorf("left %+v, want %s with %q", l, x, leftNotSynced)
	}
	if !endsReleased(chain(t, y), "s-y") || !storeEndsReleased(srv, y) {
		t.Errorf("the in-sync item's seat was not released and pushed")
	}
	lines := doctorLog(t)
	if len(lines) != 1 || lines[0].result != resultReleased || !strings.Contains(lines[0].issue, y) {
		t.Errorf("doctor log %+v, want one released line for %s", lines, y)
	}
	if st := syncStateOf(t); !slices.ContainsFunc(st.SyncForked, func(f string) bool { return strings.HasPrefix(f, x+" ") }) {
		t.Errorf("B's sync record %+v does not name %s as forked", st, x)
	}
	wantListed(t, b.dir, "seats", true)

	a.use(t)
	aChain := chain(t, x)
	b.use(t)
	bChain := chain(t, x)
	if err := runtimeLedger(t).Replace(x, bChain[len(bChain)-1].Hash, aChain); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "set", x, "--status", "done")
	staleSeat(t, x, "s-x", machine, host)
	autosync(t, b)
	if !endsReleased(chain(t, x), "s-x") || !storeEndsReleased(srv, x) {
		t.Errorf("%s: not released and pushed once in sync", x)
	}
	if _, ok := leftSeat(syncStateOf(t).Unattended, x, "s-x"); ok {
		t.Errorf("%s: still left after the release", x)
	}
}

func TestAutosyncPushesAReleaseTheNextPass(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "closed", "--criteria", "c")
	must(t, "story", "set", id, "--status", "done")
	machine, host := thisMachine(t)
	staleSeat(t, id, "s-c", machine, host)
	must(t, "ledger", "push")

	srv.FailMethods("Chain/Append")
	if out, err := run(t, "", "ledger", "autosync"); err == nil {
		t.Fatalf("autosync with the append failing exited 0:\n%s", out)
	}
	local := chain(t, id)
	if !endsReleased(local, "s-c") {
		t.Fatalf("the release was not appended locally")
	}
	if storeEndsReleased(srv, id) {
		t.Fatalf("the store holds the release although its append failed")
	}
	if st := syncStateOf(t); !strings.Contains(st.SyncReason, "Unavailable") {
		t.Errorf("sync reason %q, want the push's unavailable store", st.SyncReason)
	}
	lines := doctorLog(t)
	if len(lines) != 1 || lines[0].result != resultReleased {
		t.Errorf("doctor log %+v, want one released line", lines)
	}
	if _, ok := leftSeat(syncStateOf(t).Unattended, id, "s-c"); ok {
		t.Errorf("the released seat is recorded as left")
	}

	srv.FailMethods()
	must(t, "ledger", "autosync")
	if got := chain(t, id); len(got) != len(local) || !slices.Equal(hashes(got), storeHashes(srv, id)) {
		t.Errorf("after the next pass: local %d events (was %d), store %d", len(got), len(local), len(srv.Events("shared", id)))
	}
	if st := syncStateOf(t); st.SyncReason != "" || st.SyncPushed != 1 {
		t.Errorf("sync record %+v, want no reason and 1 pushed", st)
	}
	if again := doctorLog(t); len(again) != 1 {
		t.Errorf("the next pass wrote log lines: %+v", again)
	}
}

func skewed(f *doctorFixture) { f.running.Version = "0.9.0" }

func machineActions(u syncstate.Unattended, result string) int {
	n := 0
	for _, a := range u.Actions {
		if a.Check == "versions" && a.Result == result {
			n++
		}
	}
	return n
}

func TestAutosyncRestartsSkewedSatelled(t *testing.T) {
	f := newDoctorFixture(t)
	log := loggingSystemctl(t, f)
	other := otherRepo(t, f.dir)
	must(t, "project", "add", f.dir)
	must(t, "project", "add", other)
	skewed(f)
	for pass := 1; pass <= 2; pass++ {
		must(t, "ledger", "autosync")
		want := slices.Repeat([]string{"restart " + serviceUnitName}, pass)
		if got := systemctlCalls(t, log); !slices.Equal(got, want) {
			t.Errorf("pass %d: systemctl calls %q, want %q", pass, got, want)
		}
		for _, dir := range []string{f.dir, other} {
			if n := machineActions(recordOf(t, dir), resultApplied); n != 1 {
				t.Errorf("pass %d: %s's record holds %d applied versions action(s), want 1", pass, dir, n)
			}
		}
		lines := doctorLog(t)
		if len(lines) != pass {
			t.Fatalf("pass %d: doctor log %+v, want %d line(s)", pass, lines, pass)
		}
		for _, l := range lines {
			if l.result != resultApplied || l.project != machineProject || !strings.HasPrefix(l.issue, "versions: ") || !strings.Contains(l.issue, "differs") {
				t.Errorf("pass %d: log line %+v, want applied on - with the versions problem", pass, l)
			}
		}
	}
}

func TestAutosyncLogsAFailedRestart(t *testing.T) {
	f := newDoctorFixture(t)
	writeExec(t, filepath.Join(f.bin, "systemctl"), "#!/bin/sh\ncase \"$*\" in *is-active*) cat "+f.timerState+";; *restart*) echo 'unit refused' >&2; exit 1;; esac\n")
	registered(t)
	skewed(f)
	if out, err := run(t, "", "ledger", "autosync"); err != nil {
		t.Fatalf("a failed restart failed the pass: %v\n%s", err, out)
	}
	lines := doctorLog(t)
	if len(lines) != 1 || lines[0].result != resultFailed || !strings.Contains(lines[0].detail, "unit refused") {
		t.Errorf("doctor log %+v, want one failed line carrying systemctl's error", lines)
	}
	if n := machineActions(syncStateOf(t).Unattended, resultFailed); n != 1 {
		t.Errorf("record %+v, want the failed versions action", syncStateOf(t).Unattended)
	}
	wantListed(t, f.dir, "versions", true)
}

func TestAutosyncLeavesInstallUpdateAndAStoppedSatelled(t *testing.T) {
	cases := []struct {
		name, why, fix string
		setup          func(t *testing.T, f *doctorFixture)
	}{
		{name: "unit not installed", why: leftInstall, fix: "satelle service install", setup: func(t *testing.T, f *doctorFixture) {
			if err := os.Remove(filepath.Join(f.home, ".config", "systemd", "user", serviceUnitName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "CLI differs", why: leftNoFix, fix: "satelle update", setup: func(t *testing.T, f *doctorFixture) {
			if err := buildinfo.Record(config.GlobalDir(), filepath.Join(f.bin, "satelle"), "satelle 0.9.0 (commit cccccccccccc, built t)"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "satelled not answering", why: leftStopped, fix: "satelle service restart", setup: func(t *testing.T, f *doctorFixture) {
			writeFile(t, filepath.Join(config.GlobalDir(), config.GlobalConfigName), fmt.Sprintf("[service]\nport = %d\n", closedPort(t)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDoctorFixture(t)
			log := loggingSystemctl(t, f)
			tc.setup(t, f)
			registered(t)
			must(t, "ledger", "autosync")
			if got := systemctlCalls(t, log); len(got) != 0 {
				t.Errorf("systemctl calls %q, want none", got)
			}
			u := syncStateOf(t).Unattended
			if !slices.ContainsFunc(u.Left, func(l syncstate.UnattendedIssue) bool {
				return l.Check == "versions" && l.Why == tc.why && strings.Contains(l.Fix, tc.fix)
			}) {
				t.Errorf("left %+v, want a versions issue with %q and %q", u.Left, tc.why, tc.fix)
			}
			if len(u.Actions) != 0 {
				t.Errorf("actions %+v, want none", u.Actions)
			}
			if lines := doctorLog(t); len(lines) != 0 {
				t.Errorf("doctor log %+v, want no line", lines)
			}
			wantListed(t, f.dir, "versions", true)
		})
	}
}

func TestAutosyncLeavesSeatsUnderInvalidConfig(t *testing.T) {
	f := newDoctorFixture(t)
	registered(t)
	id := createID(t, "story", "create", "--title", "gone session", "--criteria", "c")
	ownedSeat(t, id, "s-gone", killedProc(t))
	toml := filepath.Join(f.dir, ".satelle", "satelle.toml")
	body, err := os.ReadFile(toml)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, toml, string(body)+"\n[mystery]\nx = 1\n")
	if out, err := run(t, "", "ledger", "autosync"); ExitCodeOf(err) != ExitError {
		t.Fatalf("exit %d, want %d: %v\n%s", ExitCodeOf(err), ExitError, err, out)
	}
	if s := seatsOn(t, id); len(s) != 1 {
		t.Errorf("seats %+v, want s-gone still bound", s)
	}
	l, ok := leftSeat(syncStateOf(t).Unattended, id, "s-gone")
	if !ok || l.Why != leftConfig || !strings.HasPrefix(l.Fix, configFirst) {
		t.Errorf("left %+v, want the seat with %q and a fix starting %q", l, leftConfig, configFirst)
	}
	if lines := doctorLog(t); len(lines) != 0 {
		t.Errorf("doctor log %+v, want no line", lines)
	}
	wantListed(t, f.dir, "seats", true)
}

func TestSessionStartListsOnlyWhatThePassLeft(t *testing.T) {
	t.Run("released seats leave the notice; other issues stay", func(t *testing.T) {
		f := newDoctorFixture(t)
		registered(t)
		writeFile(t, filepath.Join(f.dir, "FAIL"), "one\n")
		id := createID(t, "story", "create", "--title", "finished", "--criteria", "c")
		must(t, "story", "set", id, "--status", "done")
		staleSeat(t, id, "s-t", "", "")
		wantListed(t, f.dir, "seats", true)
		wantListed(t, f.dir, "check:fixture-check", true)
		must(t, "ledger", "autosync")
		wantListed(t, f.dir, "seats", false)
		wantListed(t, f.dir, "check:fixture-check", true)
		if _, msg, _ := hookContextText(t, f.dir); !strings.HasPrefix(msg, "satelle doctor: 1 issue(s)") {
			t.Errorf("notice header does not count the one remaining issue:\n%s", msg)
		}
	})
	t.Run("a clean repo after the pass has no notice; a seat made since waits for the next pass", func(t *testing.T) {
		f := newDoctorFixture(t)
		registered(t)
		id := createID(t, "story", "create", "--title", "finished", "--criteria", "c")
		must(t, "story", "set", id, "--status", "done")
		staleSeat(t, id, "s-t", "", "")
		must(t, "ledger", "autosync")
		if ctx, _, raw := hookContextText(t, f.dir); strings.Contains(ctx, "satelle doctor:") || strings.Contains(raw, "systemMessage") {
			t.Errorf("session start after a clean pass printed a doctor block or notice:\n%s", raw)
		}
		later := createID(t, "story", "create", "--title", "open", "--criteria", "c")
		ownedSeat(t, later, "s-later", killedProc(t))
		wantListed(t, f.dir, "seats", false)
		must(t, "ledger", "autosync")
		if !endsReleased(chain(t, later), "s-later") {
			t.Errorf("the next pass did not release the seat made after the last one")
		}
	})
	t.Run("a stale record filters nothing", func(t *testing.T) {
		f := newDoctorFixture(t)
		registered(t)
		must(t, "ledger", "autosync")
		id := createID(t, "story", "create", "--title", "open", "--criteria", "c")
		ownedSeat(t, id, "s-gone", killedProc(t))
		r, err := findRepo("")
		if err != nil {
			t.Fatal(err)
		}
		if err := syncstate.RecordUnattended(config.GlobalDir(), uiRepoRoot(r), syncstate.Unattended{At: time.Now().Add(-mirror.StaleAfter - time.Minute)}); err != nil {
			t.Fatal(err)
		}
		wantListed(t, f.dir, "seats", true)
	})
	t.Run("a restart that did not cure stays listed until the builds match", func(t *testing.T) {
		f := newDoctorFixture(t)
		loggingSystemctl(t, f)
		registered(t)
		skewed(f)
		must(t, "ledger", "autosync")
		wantListed(t, f.dir, "versions", true)
		f.running = doctorBuild
		wantListed(t, f.dir, "versions", false)
	})
	t.Run("a skew that arose after a clean pass waits for the next pass", func(t *testing.T) {
		f := newDoctorFixture(t)
		loggingSystemctl(t, f)
		registered(t)
		must(t, "ledger", "autosync")
		skewed(f)
		wantListed(t, f.dir, "versions", false)
	})
	t.Run("issues the pass never fixes are listed before and after it", func(t *testing.T) {
		f := newDoctorFixture(t)
		loggingSystemctl(t, f)
		writeFile(t, filepath.Join(config.GlobalDir(), config.GlobalConfigName), fmt.Sprintf("[service]\nport = %d\n", closedPort(t)))
		registered(t)
		id := createID(t, "story", "create", "--title", "worktree gone", "--criteria", "c")
		_, location := missingWorktree(t)
		seatAt(t, id, "s-ask", "", "", location)
		for _, when := range []string{"before", "after"} {
			if when == "after" {
				must(t, "ledger", "autosync")
				if u := syncStateOf(t).Unattended; len(u.Left) < 2 {
					t.Fatalf("the pass recorded left %+v, want the ask-class seat and the stopped satelled", u.Left)
				}
			}
			for _, check := range []string{"seats", "versions"} {
				if inCtx, inMsg := sessionLists(t, f.dir, check); !inCtx || !inMsg {
					t.Errorf("%s the pass: %s listed in context %v, notice %v; want both", when, check, inCtx, inMsg)
				}
			}
		}
	})
}

func TestLeftForSession(t *testing.T) {
	now := time.Now()
	seat := doctorIssue{Check: "seats", Problem: "p", Resolve: resolveReleaseSeat, item: "sto_1", session: "s"}
	skew := doctorIssue{Check: "versions", Problem: "running differs", Resolve: resolveRestartService, skew: true}
	stopped := doctorIssue{Check: "versions", Problem: "not answering", Resolve: resolveRestartService}
	ask := doctorIssue{Check: "seats", Problem: "q", Resolve: resolveAsk, item: "sto_2", session: "s"}
	other := doctorIssue{Check: "check:x", Problem: "r", Resolve: resolveFixItem}
	all := []doctorIssue{seat, skew, stopped, ask, other}
	cases := []struct {
		name string
		u    syncstate.Unattended
		want []doctorIssue
	}{
		{"no record", syncstate.Unattended{}, all},
		{"stale record", syncstate.Unattended{At: now.Add(-mirror.StaleAfter - time.Second)}, all},
		{"fresh record naming nothing", syncstate.Unattended{At: now}, []doctorIssue{stopped, ask, other}},
		{"seat left", syncstate.Unattended{At: now, Left: []syncstate.UnattendedIssue{{Check: "seats", Key: "sto_1 s"}}}, []doctorIssue{seat, stopped, ask, other}},
		{"seat released", syncstate.Unattended{At: now, Actions: []syncstate.UnattendedAction{{Check: "seats", Key: "sto_1 s", Result: resultReleased}}}, []doctorIssue{stopped, ask, other}},
		{"seat release failed", syncstate.Unattended{At: now, Actions: []syncstate.UnattendedAction{{Check: "seats", Key: "sto_1 s", Result: resultFailed}}}, []doctorIssue{seat, stopped, ask, other}},
		{"restart applied", syncstate.Unattended{At: now, Actions: []syncstate.UnattendedAction{{Check: "versions", Key: "running differs", Result: resultApplied}}}, []doctorIssue{skew, stopped, ask, other}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := doctorReport{Issues: all, Unchecked: []string{"u"}, Notes: []string{"n"}}
			got := leftForSession(rep, tc.u, now)
			if !slices.EqualFunc(got.Issues, tc.want, func(a, b doctorIssue) bool { return a.Check == b.Check && a.Problem == b.Problem }) {
				t.Errorf("issues %+v, want %+v", got.Issues, tc.want)
			}
			if len(got.Unchecked) != 1 || len(got.Notes) != 1 {
				t.Errorf("unchecked %v and notes %v were changed", got.Unchecked, got.Notes)
			}
		})
	}
}
