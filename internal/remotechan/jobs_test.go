package remotechan

import (
	"context"
	"testing"
	"time"
)

// lastBeat reads the receiving end's record of when job last beat.
func lastBeat(s *Session, job string) (time.Time, bool) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	at, ok := s.jobs.last[job]
	return at, ok
}

// step moves the clock one HeartbeatInterval.
func step(c *fakeClock) { c.Advance(HeartbeatInterval) }

// waitBeat waits until local has recorded a heartbeat for job stamped at now.
func waitBeat(t *testing.T, local *Session, c *fakeClock, job string) {
	t.Helper()
	eventually(t, "a heartbeat stamped at the current time", func() bool {
		at, ok := lastBeat(local, job)
		return ok && at.Equal(c.Now())
	})
}

func noLoss(t *testing.T, local *Session) {
	t.Helper()
	select {
	case l := <-local.Lost():
		t.Fatalf("job %q reported lost", l.Job)
	default:
	}
}

// AC4: a job that stops heartbeating is reported lost while its connection
// stays up; a job that keeps heartbeating, or finished, is not.
func TestStalledJobReportedLost(t *testing.T) {
	h := newHarness(t, true)
	local, remote, _ := h.connect(t)
	ctx := testCtx(t)

	local.Expect("healthy")
	local.Expect("stalls")
	local.Expect("finishes")
	healthy := remote.StartJob(context.Background(), "healthy")
	stalls := remote.StartJob(context.Background(), "stalls")
	finishes := remote.StartJob(context.Background(), "finishes")
	for _, j := range []string{"healthy", "stalls", "finishes"} {
		waitBeat(t, local, h.clock, j)
	}

	// Well past LostAfter in total, but every job beats on schedule.
	for i := 0; i < 2*int(LostAfter/HeartbeatInterval); i++ {
		step(h.clock)
		for _, j := range []string{"healthy", "stalls", "finishes"} {
			waitBeat(t, local, h.clock, j)
		}
		noLoss(t, local)
	}

	if err := finishes.Done(); err != nil {
		t.Fatal(err)
	}
	stalls.halt() // the job hangs; the connection does not
	stalledAt, _ := lastBeat(local, "stalls")

	// Step until fake time crosses LostAfter since the last beat. Before that, a
	// loss already delivered is premature. On the crossing step the clock is held
	// while the monitor reports, so however late it runs it reads the same instant.
	var lost JobLost
	steps := 0
	for lost.Job == "" {
		if steps++; steps > int((LostAfter+HeartbeatInterval)/HeartbeatInterval) {
			t.Fatalf("stalled job not reported within %v of its last heartbeat", LostAfter+HeartbeatInterval)
		}
		step(h.clock)
		waitBeat(t, local, h.clock, "healthy")
		if h.clock.Now().Sub(stalledAt) <= LostAfter {
			select {
			case l := <-local.Lost():
				t.Fatalf("lost = %+v reported %v after the last heartbeat, at most %v", l, h.clock.Now().Sub(stalledAt), LostAfter)
			default:
			}
			continue
		}
		select {
		case lost = <-local.Lost():
		case <-time.After(hangBound):
			t.Fatalf("stalled job not reported once %v had passed since its last heartbeat", h.clock.Now().Sub(stalledAt))
		}
	}
	if lost.Job != "stalls" || !lost.LastBeat.Equal(stalledAt) {
		t.Fatalf("lost = %+v, want job stalls last beating at %v", lost, stalledAt)
	}
	if waited := h.clock.Now().Sub(stalledAt); waited <= LostAfter || waited > LostAfter+HeartbeatInterval {
		t.Errorf("reported %v after the last heartbeat, want within (%v, %v]", waited, LostAfter, LostAfter+HeartbeatInterval)
	}

	// The connection was up the whole time: it still carries traffic, and only
	// the stalled job was lost.
	select {
	case <-local.Done():
		t.Fatal("the connection ended; the loss must be detected over a live connection")
	default:
	}
	if err := remote.Send(ctx, "healthy", []byte("still here")); err != nil {
		t.Fatal(err)
	}
	if m, err := local.Recv(ctx); err != nil || string(m.Body) != "still here" {
		t.Fatalf("Recv over the live connection = %+v, %v", m, err)
	}
	noLoss(t, local)
	_ = healthy.Done()
}

// A job that never sends its first heartbeat is lost too: Expect starts its clock.
func TestJobThatNeverBeatsIsLost(t *testing.T) {
	h := newHarness(t, true)
	local, _, _ := h.connect(t)
	h.clock.waitTickers(t, 2) // both sessions' loss monitors
	local.Expect("silent")
	for i := 0; i < int((LostAfter+HeartbeatInterval)/HeartbeatInterval); i++ {
		step(h.clock)
	}
	select {
	case l := <-local.Lost():
		if l.Job != "silent" {
			t.Fatalf("lost = %+v", l)
		}
	case <-time.After(hangBound):
		t.Fatal("a job that never beat was not reported lost")
	}
}

func TestJobTableReportsEachLossOnce(t *testing.T) {
	tab := newJobTable()
	t0 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	tab.beat("a", t0)
	if got := tab.lost(t0.Add(LostAfter)); len(got) != 0 {
		t.Fatalf("lost at exactly LostAfter = %v, want none", got)
	}
	if got := tab.lost(t0.Add(LostAfter + time.Nanosecond)); len(got) != 1 || got[0].Job != "a" {
		t.Fatalf("lost past LostAfter = %v", got)
	}
	if got := tab.lost(t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("a loss reported twice: %v", got)
	}
	tab.beat("b", t0)
	tab.done("b")
	if got := tab.lost(t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("a finished job reported lost: %v", got)
	}
}
