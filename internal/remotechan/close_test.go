package remotechan

import (
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bobmcallan/satelle/internal/remotechan/relaytest"
)

// Ending a session (sty_e18dfcf8): Close writes a normal close frame before it
// cancels the read loop's context, so the relay passes the end on as normal and
// the other end finishes instead of redialling for up to ResumeWindow.

// closeSessions is how many sessions TestCloseSendsNormalClosure ends. Which of
// Close's frame and the read context's cancellation reaches the socket first was
// decided by scheduling, so the proof is many ends, every one normal.
const closeSessions = 1000

// departure waits for the relay to record how session's first end left.
func departure(t *testing.T, h *harness, session string) relaytest.Departure {
	t.Helper()
	var got relaytest.Departure
	eventually(t, "the relay to record "+session+"'s departure", func() bool {
		for _, d := range h.relay.Departures() {
			if d.Session == session {
				got = d
				return true
			}
		}
		return false
	})
	return got
}

// (AC1) Ending a session from either end leaves the relay with a normal close,
// session after session, over one relay.
func TestCloseSendsNormalClosure(t *testing.T) {
	h := newHarness(t, true)
	abnormal := 0
	for i := range closeSessions {
		local, remote, spec := h.connect(t)
		closer, role := local, "local"
		if i%2 == 1 {
			closer, role = remote, "remote"
		}
		if err := closer.Close(); err != nil {
			t.Errorf("session %d: %s Close = %v", i, role, err)
		}
		d := departure(t, h, spec.Session)
		if d.Role != role {
			t.Fatalf("session %d: the relay saw %s leave first, want %s", i, d.Role, role)
		}
		if d.Status != websocket.StatusNormalClosure {
			abnormal++
			if abnormal <= 5 {
				t.Errorf("session %d: %s left with close status %v, want %v", i, role, d.Status, websocket.StatusNormalClosure)
			}
		}
	}
	if abnormal > 0 {
		t.Errorf("%d of %d sessions ended without a normal close", abnormal, closeSessions)
	}
}

// (AC2) When the controller ends the session, the node's end finishes on the
// relay's normal close: it never redials, and nothing advances the clock.
func TestPeerCloseEndsWithoutResume(t *testing.T) {
	h := newHarness(t, true)
	rec := &memRecorder{}
	local, remote := h.pair(t, "nod_end", rec, WithResumeWindow(time.Minute))
	exchange(t, local, remote)
	upgrades := h.relay.Upgrades()

	if err := local.Close(); err != nil {
		t.Fatalf("local Close = %v", err)
	}
	select {
	case <-remote.Done():
	case <-time.After(hangBound):
		t.Fatalf("the remote did not end after the local Close")
	}

	if got := h.relay.Upgrades(); got != upgrades {
		t.Errorf("upgrades = %d after the close, want %d: an end redialled", got, upgrades)
	}
	if err := local.Err(); err != ErrClosed {
		t.Errorf("local Err = %v, want %v", err, ErrClosed)
	}
	err := remote.Err()
	if !errors.Is(err, ErrClosed) || errors.Is(err, ErrResumeExpired) || errors.Is(err, ErrResumeRefused) {
		t.Errorf("remote Err = %v, want an ordinary end (ErrClosed), not a failed resume", err)
	}
	for _, kind := range []string{KindRemoteChannelResumed, KindRemoteChannelResumeFailed} {
		if n := rec.count(kind); n != 0 {
			t.Errorf("%d %s rows, want none: the end entered resume", n, kind)
		}
	}
}
