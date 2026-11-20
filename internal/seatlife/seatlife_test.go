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

func TestResolveTakesTheHooksCommonParent(t *testing.T) {
	home := t.TempDir()
	x := parent(t)
	if err := Start(home, "s1", []procgroup.Proc{killed(t), procgroup.Self(), x}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := StandingOf(home, here, seat("s1")); st != Unverified {
		t.Fatalf("before the prompt hook: %s, want unverified (a hook-depth ancestor runs)", st)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), procgroup.Self(), x}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o == nil || *o != procgroup.Self() {
		t.Fatalf("owner %+v, want %+v", o, procgroup.Self())
	}
	if st, ev := StandingOf(home, here, seat("s1")); st != Here {
		t.Fatalf("standing %s (%s), want here", st, ev)
	}
}

func TestResolveIgnoresAnAncestorBeyondHookDepth(t *testing.T) {
	home := t.TempDir()
	x := parent(t)
	if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t), x}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), x}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v taken beyond hook depth", o)
	}
}

func TestStartKeepsALiveOwnerOutsideItsChain(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", procgroup.Self()); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o == nil || *o != procgroup.Self() {
		t.Fatalf("owner %+v, want self kept", o)
	}
	if st, ev := StandingOf(home, here, seat("s1")); st != Here {
		t.Fatalf("standing %s (%s), want here", st, ev)
	}
}

func TestStartClearsAGoneOwner(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", killed(t)); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, "s1", []procgroup.Proc{procgroup.Self()}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v kept after it exited", o)
	}
}

func TestResolveClearsAGoneOwner(t *testing.T) {
	home := t.TempDir()
	if err := Own(home, "s1", killed(t)); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t)}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if o := owner(t, home, "s1"); o != nil {
		t.Fatalf("owner %+v kept after it exited", o)
	}
}

func TestSessionStartAgainKeepsTheSessionLive(t *testing.T) {
	self := procgroup.Self()
	t.Run("settled then re-read", func(t *testing.T) {
		home := t.TempDir()
		x := parent(t)
		if err := Start(home, "s1", []procgroup.Proc{killed(t), self, x}, Meta{}); err != nil {
			t.Fatal(err)
		}
		if err := Resolve(home, "s1", []procgroup.Proc{killed(t), self, x}, Meta{}); err != nil {
			t.Fatal(err)
		}
		if o := owner(t, home, "s1"); o == nil || *o != self {
			t.Fatalf("owner %+v, want %+v", o, self)
		}
		if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}, Meta{}); err != nil {
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
			if err := Start(home, "s1", []procgroup.Proc{killed(t), self}, Meta{}); err != nil {
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
		if err := Start(home, "s1", []procgroup.Proc{killed(t), self}, Meta{}); err != nil {
			t.Fatal(err)
		}
		if err := Start(home, "s1", []procgroup.Proc{killed(t), killed(t)}, Meta{}); err != nil {
			t.Fatal(err)
		}
		if err := Resolve(home, "s1", []procgroup.Proc{killed(t), self}, Meta{}); err != nil {
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

func TestAHookSettledOwnerThatExitsIsGone(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	o := procgroup.Proc{PID: cmd.Process.Pid, Start: start}
	if err := Start(home, "s1", []procgroup.Proc{killed(t), o}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", []procgroup.Proc{killed(t), o}, Meta{}); err != nil {
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
	if err := Start(home, "live", []procgroup.Proc{procgroup.Self()}, Meta{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := StandingOf(home, here, seat("live")); st != Unverified {
		t.Errorf("an alive ancestor: %s, want unverified", st)
	}
	if rec, _ := Load(home, "live"); rec.Lapsed() {
		t.Error("a record with a hook process running is lapsed")
	}
	if err := Start(home, "dead", []procgroup.Proc{killed(t), killed(t), procgroup.Self()}, Meta{}); err != nil {
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
		if err := Start(home, s, []procgroup.Proc{killed(t), killed(t)}, Meta{}); err != nil {
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

func TestSessionMetaPersistenceAndBackfill(t *testing.T) {
	home := t.TempDir()

	// Resolve with empty meta and no prior record creates nothing.
	if err := Resolve(home, "s-absent", nil, Meta{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(home, "s-absent"); ok {
		t.Fatal("Resolve with empty meta created a record")
	}

	// Resolve with non-empty meta creates a record even with no ancestor chain.
	if err := Resolve(home, "s-prompt", nil, Meta{Harness: "cursor", Model: "gemini-3.8-flash-high"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := Load(home, "s-prompt")
	if !ok || rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash-high" {
		t.Fatalf("after Resolve create: ok=%v rec=%+v", ok, rec)
	}

	// Start records harness and model even when chain is empty.
	if err := Start(home, "s1", nil, Meta{Harness: "cursor", Model: "gemini-3.8-flash-high"}); err != nil {
		t.Fatal(err)
	}
	rec, ok = Load(home, "s1")
	if !ok || rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash-high" {
		t.Fatalf("after Start: ok=%v rec=%+v", ok, rec)
	}
	atAfterStart := rec.At

	// ObserveMeta does not overwrite Start's harness or model and skips rewriting.
	if err := ObserveMeta(home, "s1", Meta{Harness: "claude", Model: "gemini-3.8-flash"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = Load(home, "s1")
	if rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash-high" || !rec.At.Equal(atAfterStart) {
		t.Fatalf("ObserveMeta clobbered existing meta: %+v (want At %v)", rec, atAfterStart)
	}

	// Start/Resolve with empty Model preserves existing Model.
	if err := Start(home, "s1", nil, Meta{Harness: "cursor"}); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(home, "s1", nil, Meta{Harness: "cursor"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = Load(home, "s1")
	if rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash-high" {
		t.Fatalf("empty incoming model cleared stored model: %+v", rec)
	}

	// Resolve updates Model on a mid-session model switch.
	if err := Resolve(home, "s1", nil, Meta{Harness: "cursor", Model: "claude-opus-5-5-high"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = Load(home, "s1")
	if rec.Harness != "cursor" || rec.Model != "claude-opus-5-5-high" {
		t.Fatalf("Resolve did not update model: %+v", rec)
	}

	// ObserveMeta backfills missing model without overwriting harness, and creates if absent.
	if err := Start(home, "s-backfill", nil, Meta{Harness: "cursor"}); err != nil {
		t.Fatal(err)
	}
	if err := ObserveMeta(home, "s-backfill", Meta{Harness: "claude", Model: "gemini-3.8-flash"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = Load(home, "s-backfill")
	if rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash" {
		t.Fatalf("ObserveMeta backfill: %+v", rec)
	}
	if err := ObserveMeta(home, "s-new", Meta{Harness: "cursor", Model: "gemini-3.8-flash"}); err != nil {
		t.Fatal(err)
	}
	rec, ok = Load(home, "s-new")
	if !ok || rec.Harness != "cursor" || rec.Model != "gemini-3.8-flash" {
		t.Fatalf("ObserveMeta on absent record: ok=%v rec=%+v", ok, rec)
	}
}
