//go:build linux || darwin

package seatlife

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

// killed starts a child, kills and reaps it, and returns it: a process that
// provably exited.
func killed(t *testing.T) procgroup.Proc {
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

func parent(t *testing.T) procgroup.Proc {
	t.Helper()
	a := procgroup.Ancestors(os.Getpid())
	if len(a) == 0 {
		t.Skip("no process ancestry on this platform")
	}
	return a[0]
}

const here = "m-here"

func seat(session string) item.Seat {
	return item.Seat{Item: "sto_x", Session: session, Location: "local:/tmp/x", Machine: here, Host: "h"}
}

func owner(t *testing.T, home, session string) *procgroup.Proc {
	t.Helper()
	r, ok := Load(home, session)
	if !ok {
		t.Fatalf("no record for %s", session)
	}
	return r.Owner
}

// Two hook calls of one harness run through different shells; the harness is
// their common parent within hook depth, and becomes the owner.
func TestResolveTakesTheHooksCommonParent(t *testing.T) {
	home := t.TempDir()
	x := parent(t)
	if err := Start(home, "s1", []procgroup.Proc{killed(t), procgroup.Self(), x}); err != nil {
		t.Fatal(err)
	}
	if st, _ := StandingOf(home, here, seat("s1")); st != Unverified {
		t.Fatalf("before the prompt hook: %s, want unverified (a hook-depth ancestor runs)", st)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), procgroup.Self(), x}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o == nil || *o != procgroup.Self() {
		t.Fatalf("owner %+v, want %+v", o, procgroup.Self())
	}
	if st, ev := StandingOf(home, here, seat("s1")); st != Here {
		t.Fatalf("standing %s (%s), want here", st, ev)
	}
}

// A common ancestor deeper than hook depth (a terminal) is never the owner.
func TestResolveIgnoresAnAncestorBeyondHookDepth(t *testing.T) {
	home := t.TempDir()
	x := parent(t)
	if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t), x}); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), x}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v taken beyond hook depth", o)
	}
}

// A dispatched run's satelle process owns its session; a later session
// start keeps it while it is alive, whether or not it is in the new chain
// (sto_35c85724 AC2).
func TestStartKeepsALiveOwnerOutsideItsChain(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o == nil || *o != procgroup.Self() {
		t.Fatalf("owner %+v, want self kept", o)
	}
	if st, ev := StandingOf(home, here, seat("s1")); st != Here {
		t.Fatalf("standing %s (%s), want here", st, ev)
	}
}

// A resumed session runs in a new process: its session start clears an owner
// that exited.
func TestStartClearsAGoneOwner(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", killed(t)); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, "s1", []procgroup.Proc{procgroup.Self()}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v kept after it exited", o)
	}
}

// A later hook call clears an owner that is not alive, as a session start
// does (sto_35c85724 AC2).
func TestResolveClearsAGoneOwner(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", killed(t)); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t)}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v kept after it exited", o)
	}
}

// A SessionStart that fires again in the same session with different hook
// pids (a context re-read) leaves the session live while its owner runs
// (sto_35c85724 AC3).
func TestSessionStartAgainKeepsTheSessionLive(t *testing.T) {
	self := procgroup.Self()
	t.Run("settled then re-read", func(t *testing.T) {
		home := t.TempDir()
		x := parent(t)
		if err := Start(home, "s1", []procgroup.Proc{killed(t), self, x}); err != nil {
			t.Fatal(err)
		}
		if err := Resolve(home, "s1", []procgroup.Proc{killed(t), self, x}); err != nil {
			t.Fatal(err)
		}
		if o := owner(t, home, "s1"); o == nil || *o != self {
			t.Fatalf("owner %+v, want %+v", o, self)
		}
		if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}); err != nil {
			t.Fatal(err)
		}
		if o := owner(t, home, "s1"); o == nil || *o != self {
			t.Fatalf("owner %+v after the re-read, want %+v kept", o, self)
		}
		if st, ev := StandingOf(home, here, seat("s1")); st != Here {
			t.Fatalf("standing %s (%s), want here", st, ev)
		}
	})
	t.Run("twice before any prompt", func(t *testing.T) {
		home := t.TempDir()
		for range 2 {
			if err := Start(home, "s1", []procgroup.Proc{killed(t), self}); err != nil {
				t.Fatal(err)
			}
		}
		if o := owner(t, home, "s1"); o == nil || *o != self {
			t.Fatalf("owner %+v, want %+v", o, self)
		}
		if st, ev := StandingOf(home, here, seat("s1")); st != Here {
			t.Fatalf("standing %s (%s), want here", st, ev)
		}
	})
	t.Run("a re-read from another tree", func(t *testing.T) {
		home := t.TempDir()
		if err := Start(home, "s1", []procgroup.Proc{killed(t), self}); err != nil {
			t.Fatal(err)
		}
		if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}); err != nil {
			t.Fatal(err)
		}
		if err := Resolve(home, "s1", []procgroup.Proc{killed(t), self}); err != nil {
			t.Fatal(err)
		}
		if o := owner(t, home, "s1"); o == nil || *o != self {
			t.Fatalf("owner %+v, want %+v", o, self)
		}
		if st, ev := StandingOf(home, here, seat("s1")); st != Here {
			t.Fatalf("standing %s (%s), want here", st, ev)
		}
	})
}

// An owner settled from two hook calls makes its session gone once it exits,
// and the gone record is pruned (sto_35c85724 AC3).
func TestAHookSettledOwnerThatExitsIsGone(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	o := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	if err := Start(home, "s1", []procgroup.Proc{killed(t), o}); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), o}); err != nil {
		t.Fatal(err)
	}
	if got := owner(t, home, "s1"); got == nil || *got != o {
		t.Fatalf("owner %+v, want %+v", got, o)
	}
	if st, ev := StandingOf(home, here, seat("s1")); st != Here {
		t.Fatalf("standing %s (%s), want here", st, ev)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	st, ev := StandingOf(home, here, seat("s1"))
	if st != Gone || !strings.Contains(string(ev), "has exited") {
		t.Fatalf("standing %s (%s), want gone with the owner exited", st, ev)
	}
	if err := Prune(home, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(home, "s1"); ok {
		t.Error("the gone record was not pruned")
	}
}

func TestStandingGoneWhenOwnerExited(t *testing.T) {
	home := t.TempDir()
	dead := killed(t)
	if err := Own(home, "s1", dead); err != nil {
		t.Fatal(err)
	}
	st, ev := StandingOf(home, here, seat("s1"))
	if st != Gone {
		t.Fatalf("standing %s (%s), want gone", st, ev)
	}
	if want := "has exited"; !strings.Contains(string(ev), want) {
		t.Errorf("evidence %q lacks %q", ev, want)
	}
}

func TestStandingWithoutOwner(t *testing.T) {
	home := t.TempDir()
	if err := Start(home, "live", []procgroup.Proc{procgroup.Self()}); err != nil {
		t.Fatal(err)
	}
	if st, _ := StandingOf(home, here, seat("live")); st != Unverified {
		t.Errorf("an alive ancestor: %s, want unverified", st)
	}
	if rec, _ := Load(home, "live"); rec.Lapsed() {
		t.Error("a record with a hook process running is lapsed")
	}
	// Only exited hook processes, and no settled owner: unverified, never
	// gone (sto_35c85724 AC1, AC3).
	if err := Start(home, "dead", []procgroup.Proc{killed(t), killed(t), procgroup.Self()}); err != nil {
		t.Fatal(err)
	}
	st, ev := StandingOf(home, here, seat("dead"))
	if st != Unverified || !strings.Contains(string(ev), "have exited") {
		t.Errorf("every hook process exited: %s (%s), want unverified with the hooks exited", st, ev)
	}
	rec, _ := Load(home, "dead")
	if rec.Gone() || !rec.Lapsed() {
		t.Errorf("gone %v lapsed %v, want not gone and lapsed", rec.Gone(), rec.Lapsed())
	}
}

// The location and machine rules come before any record.
func TestStandingByMachineAndLocation(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		seat item.Seat
		want Standing
	}{
		{item.Seat{Session: "s1", Location: "remote:n1", Machine: here}, Elsewhere},
		{item.Seat{Session: "s1", Location: "local:/x"}, Unverified},
		{item.Seat{Session: "s1", Location: "local:/x", Machine: "m-other", Host: "other-host"}, Elsewhere},
		{item.Seat{Session: "s-none", Location: "local:/x", Machine: here}, Unverified},
		{item.Seat{Session: "s1", Location: "local:/x", Machine: here}, Here},
	}
	for _, c := range cases {
		if st, ev := StandingOf(home, here, c.seat); st != c.want {
			t.Errorf("%+v: %s (%s), want %s", c.seat, st, ev, c.want)
		}
	}
}

func TestPruneKeepsSeatedSessions(t *testing.T) {
	home := t.TempDir()
	for _, s := range []string{"gone-seated", "gone-free"} {
		if err := Own(home, s, killed(t)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Own(home, "alive", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"lapsed-seated", "lapsed-free"} {
		if err := Start(home, s, []procgroup.Proc{killed(t), killed(t)}); err != nil {
			t.Fatal(err)
		}
	}
	seated := map[string]bool{"gone-seated": true, "lapsed-seated": true}
	if err := Prune(home, func(s string) bool { return seated[s] }); err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]bool{"gone-seated": true, "gone-free": false, "alive": true, "lapsed-seated": true, "lapsed-free": false} {
		if _, ok := Load(home, s); ok != want {
			t.Errorf("%s kept = %v, want %v", s, ok, want)
		}
	}
}

func TestRecordRefusesAPathSession(t *testing.T) {
	if err := Own(t.TempDir(), "../x", procgroup.Self()); err == nil {
		t.Fatal("a session naming a path was recorded")
	}
}
