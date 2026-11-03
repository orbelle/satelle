// Package relaytest is the in-process stand-in for the satelle-server relay: a
// TLS WebSocket server on loopback that pairs the two ends of a remotechan
// session by session id and forwards every binary message unchanged. It
// implements the "Relay contract" documented in package remotechan, taps every
// frame it forwards so a test can read what a real relay could read, and can be
// scripted to reject bearers with a 401. It is test support only.
package relaytest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Frame is one message the relay forwarded, with who sent it ("local" or "remote").
type Frame struct {
	From string
	Data []byte
}

// Departure is how the first end of a paired session left the relay: the close
// status its connection ended with (-1 when it vanished with no close frame).
type Departure struct {
	Session string
	Role    string
	Status  websocket.StatusCode
}

// slot is one session's two ends. ready is non-nil while a local end has been
// admitted but is not yet recorded in local (its upgrade is being answered): the
// contract's "local end waiting" starts at the admission, not at the 101.
// closing marks a slot the relay itself is closing (CloseWith, Restart, Down,
// shutdown): it is closing both ends, so an end's serve teardown must leave the
// other end alone, or it could drop that end before its close frame is written.
type slot struct {
	local, remote *websocket.Conn
	ready         chan struct{}
	closing       bool
}

// Relay is the stand-in relay. Create it with New.
type Relay struct {
	srv *httptest.Server

	mu            sync.Mutex
	slots         map[string]*slot
	conns         []*websocket.Conn
	frames        []Frame
	departures    []Departure
	bearers       []string
	remoteBearers []string
	attempts      int
	upgrades      int
	reject        int
	rejectRemote  int
	other         http.Handler

	holdArmed bool
	holds     map[string]chan struct{}

	down    bool
	delay   map[string]time.Duration
	refuse  map[string]refusal
	dropped int

	// beforeRemoteClose, when set by an in-package test, runs in CloseWith just
	// before the remote end is closed, so a test can pin the ordering of the two
	// closes against the serve teardown.
	beforeRemoteClose func(session string)
}

// refusal is a scripted answer to every upgrade of one session.
type refusal struct {
	status int
	body   string
}

// New starts a relay for the life of t.
func New(t testing.TB) *Relay {
	t.Helper()
	r := &Relay{
		slots: map[string]*slot{}, holds: map[string]chan struct{}{},
		delay: map[string]time.Duration{}, refuse: map[string]refusal{},
	}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(func() {
		r.mu.Lock()
		r.markClosing()
		conns := append([]*websocket.Conn(nil), r.conns...)
		r.mu.Unlock()
		for _, c := range conns {
			c.CloseNow()
		}
		r.srv.Close()
	})
	return r
}

// URL is the relay base URL to hand to remotechan (https; the dialer upgrades it).
func (r *Relay) URL() string { return r.srv.URL + "/relay" }

// Client is an HTTP client that trusts the relay's test certificate.
func (r *Relay) Client() *http.Client { return r.srv.Client() }

// RejectBearers makes the next n local upgrades fail with 401 whatever bearer
// they carry, so a test can exercise the refresh-and-retry.
func (r *Relay) RejectBearers(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reject = n
}

// HoldLocalUntilRemote makes the next admitted local upgrade wait, after its 101
// is written and before the relay records it, until a remote upgrade request for
// the same session reaches the relay (or the local's request ends). It pins the
// window in which a launched remote can arrive before the local end is recorded,
// so a test can prove the relay pairs it instead of refusing it.
func (r *Relay) HoldLocalUntilRemote() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holdArmed = true
}

// RejectRemoteBearers makes the next n remote upgrades fail with 401 whatever
// bearer they carry, as a relay does for a node token it does not accept.
func (r *Relay) RejectRemoteBearers(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejectRemote = n
}

// Attempts counts every upgrade request the relay received, rejected or not.
func (r *Relay) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Upgrades counts the connections the relay accepted.
func (r *Relay) Upgrades() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upgrades
}

// Bearers lists the bearer each local upgrade request carried, in order.
func (r *Relay) Bearers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bearers...)
}

// RemoteBearers lists the bearer each remote upgrade request carried, in order.
func (r *Relay) RemoteBearers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.remoteBearers...)
}

// Dropped counts the messages the relay read while the session's other end was
// absent and so could not forward.
func (r *Relay) Dropped() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Restart is a relay restart that is back at once: it closes every connection
// with going-away "server shutting down" and forgets every session, so each end
// that redials is admitted afresh. Upgrades keep being answered on the same URL.
func (r *Relay) Restart() {
	r.closeAll(false)
}

// Down is a restart that has not finished: it closes every connection as Restart
// does, and answers every upgrade 503 until Up.
func (r *Relay) Down() {
	r.closeAll(true)
}

// Up ends Down.
func (r *Relay) Up() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = false
}

func (r *Relay) closeAll(down bool) {
	r.mu.Lock()
	r.down = down
	r.markClosing()
	conns := r.conns
	r.conns = nil
	r.slots = map[string]*slot{}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Close(websocket.StatusGoingAway, "server shutting down")
		}()
	}
	wg.Wait()
}

// markClosing marks every session slot as being closed by the relay. r.mu held.
func (r *Relay) markClosing() {
	for _, sl := range r.slots {
		sl.closing = true
	}
}

// DelayAdmit holds every upgrade for role ("local" or "remote") for d before the
// relay admits it, whatever the other end is doing; d of 0 ends the delay. It
// fixes which end a simultaneous redial reaches the relay first.
func (r *Relay) DelayAdmit(role string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delay[role] = d
}

// CloseWith closes both ends of session with the given close code and reason, as
// a relay refusing the session does.
func (r *Relay) CloseWith(session string, code websocket.StatusCode, reason string) {
	r.mu.Lock()
	var local, remote *websocket.Conn
	if sl := r.slots[session]; sl != nil {
		local, remote = sl.local, sl.remote
		// The serve goroutines keep their own pointer to sl, so they still see the
		// mark; a later admission of this session gets a fresh slot.
		sl.closing = true
		delete(r.slots, session)
	}
	hook := r.beforeRemoteClose
	r.mu.Unlock()
	var wg sync.WaitGroup
	closeEnd := func(c *websocket.Conn, before func(string)) {
		if c == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if before != nil {
				before(session)
			}
			c.Close(code, reason)
		}()
	}
	closeEnd(local, nil)
	closeEnd(remote, hook)
	wg.Wait()
}

// RefuseSession answers every following upgrade of session with status and body,
// as a relay does for a session it does not know (404 bad_session) or a slot
// another user holds. A status of 0 ends the refusal.
func (r *Relay) RefuseSession(session string, status int, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status == 0 {
		delete(r.refuse, session)
		return
	}
	r.refuse[session] = refusal{status: status, body: body}
}

// Frames is a copy of every message the relay forwarded, in arrival order.
func (r *Relay) Frames() []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Frame, len(r.frames))
	for i, f := range r.frames {
		out[i] = Frame{From: f.From, Data: append([]byte(nil), f.Data...)}
	}
	return out
}

// Departures lists, in order, how the first end of each paired session left: the
// end whose leaving the relay passed on to the other. The second end's leaving is
// the relay's own doing and is not listed.
func (r *Relay) Departures() []Departure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Departure(nil), r.departures...)
}

// Mount serves every path outside /relay/ with h, so a test can put a stand-in for
// the server's other routes on the host the relay is under.
func (r *Relay) Mount(h http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.other = h
}

func (r *Relay) serve(w http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.URL.Path, "/relay/") {
		r.mu.Lock()
		other := r.other
		r.mu.Unlock()
		if other != nil {
			other.ServeHTTP(w, req)
			return
		}
	}
	session := strings.TrimPrefix(req.URL.Path, "/relay/")
	role := req.URL.Query().Get("role")

	r.mu.Lock()
	r.attempts++
	down, refused, delay := r.down, r.refuse[session], r.delay[role]
	r.mu.Unlock()
	if down {
		http.Error(w, "relay restarting", http.StatusServiceUnavailable)
		return
	}
	if refused.status != 0 {
		http.Error(w, refused.body, refused.status)
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-req.Context().Done():
			return
		}
	}

	r.mu.Lock()
	if role == "local" {
		bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		r.bearers = append(r.bearers, bearer)
		if r.reject > 0 || bearer == "" {
			if r.reject > 0 {
				r.reject--
			}
			r.mu.Unlock()
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
	}
	if role == "remote" {
		// A node presents the token satelle.dev gave it; one without a token, or one
		// the test scripts the relay to refuse, is not accepted.
		bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		r.remoteBearers = append(r.remoteBearers, bearer)
		if r.rejectRemote > 0 || bearer == "" {
			if r.rejectRemote > 0 {
				r.rejectRemote--
			}
			r.mu.Unlock()
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		// A remote's arrival is signalled in the same critical section as its pairing
		// decision, so a held local cannot be recorded before the remote is judged.
		if ch := r.holds[session]; ch != nil {
			close(ch)
			delete(r.holds, session)
		}
	}
	sl := r.slots[session]
	var wait, hold chan struct{}
	switch {
	case role == "local" && (sl == nil || sl.local == nil && sl.ready == nil):
		if sl == nil {
			sl = &slot{}
			r.slots[session] = sl
		}
		sl.ready = make(chan struct{})
		if r.holdArmed {
			r.holdArmed = false
			hold = make(chan struct{})
			r.holds[session] = hold
		}
	case role == "remote" && (sl == nil || sl.remote == nil):
		// A node started by satelle.dev may reach the relay before the local end.
		if sl == nil {
			sl = &slot{}
			r.slots[session] = sl
		}
		if sl.local == nil && sl.ready != nil {
			wait = sl.ready
		}
	default:
		r.mu.Unlock()
		http.Error(w, "no such pairing", http.StatusNotFound)
		return
	}
	r.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-req.Context().Done():
			return
		}
		r.mu.Lock()
		sl = r.slots[session]
		paired := sl != nil && sl.local != nil && sl.remote == nil
		r.mu.Unlock()
		if !paired {
			http.Error(w, "no such pairing", http.StatusNotFound)
			return
		}
	}

	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		if role == "local" {
			r.release(session, sl)
		}
		return
	}
	c.SetReadLimit(1 << 20)

	if hold != nil {
		select {
		case <-hold:
		case <-req.Context().Done():
		}
	}

	r.mu.Lock()
	r.upgrades++
	r.conns = append(r.conns, c)
	if role == "local" {
		sl.local = c
		close(sl.ready)
		sl.ready = nil
	} else {
		sl.remote = c
	}
	r.mu.Unlock()

	err = r.pump(req.Context(), c, sl, role)

	r.mu.Lock()
	peer := sl.remote
	if role == "remote" {
		peer = sl.local
		sl.remote = nil
	} else {
		sl.local = nil
	}
	if peer != nil {
		r.departures = append(r.departures, Departure{Session: session, Role: role, Status: websocket.CloseStatus(err)})
	}
	closing := sl.closing
	r.mu.Unlock()
	c.CloseNow()
	// The relay closing this session is closing the peer itself, with its own code.
	if peer != nil && !closing {
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			// An end that left on purpose is passed on as such, so the other end can
			// tell it from a relay that went away.
			go peer.Close(websocket.StatusNormalClosure, "peer left")
		} else {
			peer.CloseNow()
		}
	}
}

// release drops a local reservation whose upgrade failed, waking any remote
// waiting on it (which then finds no local end and is refused).
func (r *Relay) release(session string, sl *slot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sl.ready != nil {
		close(sl.ready)
		sl.ready = nil
	}
	if sl.local == nil && sl.remote == nil && r.slots[session] == sl {
		delete(r.slots, session)
	}
}

// pump reads c until it ends, tapping each message and forwarding it to the
// other end if that end is present (a message with no peer is dropped). It
// returns the error that ended the read.
func (r *Relay) pump(ctx context.Context, c *websocket.Conn, sl *slot, role string) error {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.frames = append(r.frames, Frame{From: role, Data: append([]byte(nil), data...)})
		peer := sl.remote
		if role == "remote" {
			peer = sl.local
		}
		if peer == nil {
			r.dropped++
		}
		r.mu.Unlock()
		if peer != nil {
			_ = peer.Write(ctx, typ, data)
		}
	}
}
