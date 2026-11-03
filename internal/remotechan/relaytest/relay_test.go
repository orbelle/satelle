package relaytest

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dial(ctx context.Context, r *Relay, session, role string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, r.URL()+"/"+session+"?role="+role, &websocket.DialOptions{
		HTTPClient: r.Client(),
		HTTPHeader: http.Header{"Authorization": {"Bearer x"}},
	})
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func refusedWith(t *testing.T, resp *http.Response, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("dial succeeded, want HTTP %d", want)
	}
	if resp == nil || resp.StatusCode != want {
		t.Fatalf("dial = %v (response %v), want HTTP %d", err, resp, want)
	}
}

// send writes one binary frame at from and reads it at to.
func send(t *testing.T, ctx context.Context, from, to *websocket.Conn, msg string) {
	t.Helper()
	if err := from.Write(ctx, websocket.MessageBinary, []byte(msg)); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	typ, got, err := to.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(got) != msg {
		t.Fatalf("read = %q, %v, %v; want binary %q", got, typ, err, msg)
	}
}

// A remote that reaches the relay while the local's upgrade is still being
// recorded is paired, not refused: the local end is waiting from its admission.
func TestRemoteDuringLocalRegistrationIsPaired(t *testing.T) {
	r := New(t)
	r.HoldLocalUntilRemote()
	ctx := testCtx(t)

	local, resp, err := dial(ctx, r, "s1", "local")
	if err != nil {
		t.Fatalf("local dial: %v (response %v)", err, resp)
	}
	remote, resp, err := dial(ctx, r, "s1", "remote")
	if err != nil {
		t.Fatalf("remote dial: %v (response %v)", err, resp)
	}
	send(t, ctx, remote, local, "hello")
	send(t, ctx, local, remote, "hi")
}

// The contract's refusals still hold with a reservation in play.
func TestRelayRefusesOutsideTheContract(t *testing.T) {
	r := New(t)
	ctx := testCtx(t)

	// A remote with no local end is admitted (sty_63f30d3b): a node started by
	// satelle.dev can reach the relay first, so that is not outside the contract.
	r.HoldLocalUntilRemote()
	local, resp, err := dial(ctx, r, "s1", "local")
	if err != nil {
		t.Fatalf("local dial: %v (response %v)", err, resp)
	}
	// The first local is held, not yet recorded: a second local is still refused.
	_, resp, err = dial(ctx, r, "s1", "local")
	refusedWith(t, resp, err, http.StatusNotFound)

	remote, resp, err := dial(ctx, r, "s1", "remote")
	if err != nil {
		t.Fatalf("remote dial: %v (response %v)", err, resp)
	}
	send(t, ctx, remote, local, "ping")

	_, resp, err = dial(ctx, r, "s1", "remote") // second remote on a paired session
	refusedWith(t, resp, err, http.StatusNotFound)
	_, resp, err = dial(ctx, r, "s1", "other") // unknown role
	refusedWith(t, resp, err, http.StatusNotFound)
}

// A session the relay closes with a code reaches both ends with exactly that
// code, every time: the relay's own teardown of the first end never drops the
// second end without a frame (which a client reads as a relay restart and
// redials). The hook pins the losing ordering: the remote end is closed only
// after the local end's teardown has run.
func TestCloseWithReachesBothEnds(t *testing.T) {
	r := New(t)
	const code = websocket.StatusCode(4404)

	for i := range 1000 {
		session := fmt.Sprintf("s%d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		local, resp, err := dial(ctx, r, session, "local")
		if err != nil {
			cancel()
			t.Fatalf("iteration %d: local dial: %v (response %v)", i, err, resp)
		}
		remote, resp, err := dial(ctx, r, session, "remote")
		if err != nil {
			cancel()
			t.Fatalf("iteration %d: remote dial: %v (response %v)", i, err, resp)
		}
		// Remote first: its pump starts only once it is recorded, so its frame
		// reaching local proves the pairing before local sends back.
		send(t, ctx, remote, local, "ping")
		send(t, ctx, local, remote, "pong")

		read := func(c *websocket.Conn) (chan error, chan struct{}) {
			errc, done := make(chan error, 1), make(chan struct{})
			go func() {
				_, _, err := c.Read(ctx)
				errc <- err
				close(done)
			}()
			return errc, done
		}
		localErr, _ := read(local)
		remoteErr, remoteDone := read(remote)

		r.beforeRemoteClose = func(s string) {
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && !departed(r, s, "local") {
				time.Sleep(time.Millisecond)
			}
			// Give a teardown that reaches for the remote end the moment to do so.
			select {
			case <-remoteDone:
			case <-time.After(5 * time.Millisecond):
			}
		}
		r.CloseWith(session, code, "bad_session")

		for _, end := range []struct {
			role string
			errc chan error
		}{{"local", localErr}, {"remote", remoteErr}} {
			if got := websocket.CloseStatus(<-end.errc); got != code {
				cancel()
				t.Fatalf("iteration %d: %s end closed with status %d, want %d", i, end.role, got, code)
			}
		}
		cancel()
	}
}

func departed(r *Relay, session, role string) bool {
	for _, d := range r.Departures() {
		if d.Session == session && d.Role == role {
			return true
		}
	}
	return false
}
