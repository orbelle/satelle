package remotechan

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// ErrRelayRefused is returned when the relay answers the upgrade with a status
// other than 101 that signing in again would not fix.
var ErrRelayRefused = errors.New("remotechan: relay refused the connection")

// RelayStatusError is ErrRelayRefused with the status the relay answered the
// upgrade with, so a caller can tell a refused credential (401) from the rest.
// Body is the bounded text the relay answered with, which names bad_session or
// slot_taken when it refuses a redial for that reason.
type RelayStatusError struct {
	Status int
	Body   string
}

func (e *RelayStatusError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", ErrRelayRefused.Error(), e.Status)
}
func (e *RelayStatusError) Is(target error) bool { return target == ErrRelayRefused }

// ErrEnrollRefused is returned to a remote that local satelle refused.
var ErrEnrollRefused = errors.New("remotechan: enrollment refused")

// LaunchSpec is what local satelle hands the launcher to start a node with: the
// relay to dial, the session to join and the single-use token to enroll with.
type LaunchSpec struct {
	RelayURL string
	Session  string
	Token    string
}

// Launcher starts a node and returns the node's public key as the launch read
// it. local satelle pins that key; the key is read at launch, never from the
// channel, so a party in the middle cannot supply its own.
type Launcher interface {
	Launch(ctx context.Context, spec LaunchSpec) (nodePublicKey []byte, err error)
}

// LauncherFunc adapts a function to a Launcher.
type LauncherFunc func(ctx context.Context, spec LaunchSpec) ([]byte, error)

// Launch calls f.
func (f LauncherFunc) Launch(ctx context.Context, spec LaunchSpec) ([]byte, error) {
	return f(ctx, spec)
}

// Event kinds the channel records.
const (
	KindRemoteEnrollRefused       = "remote_enroll_refused"
	KindRemoteChannelResumed      = "remote_channel_resumed"
	KindRemoteChannelResumeFailed = "remote_channel_resume_failed"
)

// Record is one channel event for a Recorder.
type Record struct {
	Kind    string
	Actor   string
	Body    string
	Payload json.RawMessage
}

// Recorder is where channel events (refused enrollments, resumes) are written.
type Recorder interface {
	Record(ctx context.Context, r Record, now time.Time) error
}

type options struct {
	http   *http.Client
	clock  Clock
	tokens *TokenBook
	rec    Recorder
	window time.Duration
}

// Option configures Open or Join.
type Option func(*options)

// WithHTTPClient sets the client the relay dial uses (default: http.DefaultClient,
// which honours HTTPS_PROXY). The client must have no Timeout: a WebSocket is long-lived.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.http = c } }

// WithClock sets the time source (default: the real clock).
func WithClock(c Clock) Option { return func(o *options) { o.clock = c } }

// WithTokens sets the token book Open mints from and redeems against (default: a
// fresh one per Open).
func WithTokens(b *TokenBook) Option { return func(o *options) { o.tokens = b } }

// WithRecorder sets where refused enrollments, and channel resumes, are recorded
// (default: nowhere).
func WithRecorder(r Recorder) Option { return func(o *options) { o.rec = r } }

// WithResumeWindow sets how long a server-minted session (AttachServerSession,
// Join) keeps redialling after the relay drops it (default: ResumeWindow).
func WithResumeWindow(d time.Duration) Option { return func(o *options) { o.window = d } }

func newOptions(opts []Option) *options {
	o := &options{}
	for _, f := range opts {
		f(o)
	}
	if o.window <= 0 {
		o.window = ResumeWindow
	}
	if o.clock == nil {
		o.clock = RealClock()
	}
	if o.tokens == nil {
		o.tokens = NewTokenBook(o.clock)
	}
	return o
}

// Open is local satelle's end. It dials the relay with the satelle.dev bearer
// (a signed-out client returns hosted.ErrLoginRequired before anything is
// minted or launched), then mints a single-use token, launches the node, pins
// the key the launch returned, and waits for the node to enroll. The returned
// Session is admitted and sealed. It lives until Close.
//
// Only dialing out: Open never listens.
func Open(ctx context.Context, client *hosted.Client, relayURL string, launch Launcher, opts ...Option) (*Session, error) {
	o := newOptions(opts)
	session, err := newSessionID()
	if err != nil {
		return nil, err
	}
	conn, err := dialLocal(ctx, client, o, relayURL, session)
	if err != nil {
		return nil, err
	}

	token, err := o.tokens.Mint()
	if err == nil {
		var pinned []byte
		if pinned, err = launch.Launch(ctx, LaunchSpec{RelayURL: relayURL, Session: session, Token: token}); err == nil {
			var s *Session
			redeem := func(m appMsg) error { return o.tokens.Redeem(m.Token) }
			if s, err = acceptRemote(ctx, conn, session, pinned, o, redeem, nil); err == nil { // an Open session never resumes: its token is single use
				return s, nil
			}
		}
	}
	conn.Close(websocket.StatusPolicyViolation, "open failed")
	return nil, err
}

// dialLocal dials the relay as local with the satelle.dev bearer, refreshing it
// once on a 401. It is the one local dial, shared by Open and AttachServerSession.
func dialLocal(ctx context.Context, client *hosted.Client, o *options, relayURL, session string) (*websocket.Conn, error) {
	var conn *websocket.Conn
	var status int
	var refusal []byte
	err := client.WithBearer(ctx, func(ctx context.Context, bearer string) (int, []byte, error) {
		c, resp, err := dialRelay(ctx, o.http, relayURL, session, RoleLocal, bearer)
		if err != nil {
			if resp != nil {
				status = resp.StatusCode
				refusal = hosted.BoundAuthBody(resp.Body)
				return status, refusal, nil
			}
			return 0, nil, fmt.Errorf("remotechan: dial relay: %w", err)
		}
		conn, status = c, http.StatusSwitchingProtocols
		return status, nil, nil
	})
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, &RelayStatusError{Status: status, Body: string(refusal)}
	}
	return conn, nil
}

// ErrKeyUnpublished is returned by AttachServerSession when the node never
// published its channel key within the bound, so there is nothing to pin.
var ErrKeyUnpublished = errors.New("remotechan: the node published no channel key")

// keyPollInterval is how often AttachServerSession asks for the node's key while
// it is not yet published.
const keyPollInterval = time.Second

// AttachServerSession is local's end of a session satelle.dev minted: the
// session id is the node id the server started, and the relay has already
// authenticated both ends (the starter as local, the node by its node token).
// There is no local mint and no enroll token — TokenBook is never consulted.
//
// Admission is by key. keyFn reads the key the node published (nil until it
// does); it is asked until a key is present, for at most EnrollTTL on the
// options clock, and a node that never publishes one is refused
// (ErrKeyUnpublished, recorded as key_unpublished) with the session closed. The
// handshake then runs pinned to that key: a node holding any other key cannot
// derive the session keys and is refused as key_mismatch. No frame is trusted
// before the key is present and matches, and there is no trust on first use.
//
// The node side is unchanged: Join sends its bearer as the enroll value, which
// this path ignores.
//
// The session resumes: when the relay drops the connection in a way a restart
// explains, it redials the same session with backoff, for at most the resume
// window, and handshakes again against the same pinned key (the key is not asked
// for again).
func AttachServerSession(ctx context.Context, client *hosted.Client, relayURL, session string, keyFn func(context.Context) ([]byte, error), opts ...Option) (*Session, error) {
	o := newOptions(opts)
	conn, err := dialLocal(ctx, client, o, relayURL, session)
	if err != nil {
		return nil, err
	}
	pinned, err := awaitKey(ctx, o, keyFn)
	if err != nil {
		if errors.Is(err, ErrKeyUnpublished) {
			err = o.refused(ctx, session, "key_unpublished", err)
		}
		conn.Close(websocket.StatusPolicyViolation, "attach failed")
		return nil, err
	}
	admitByKey := func(appMsg) error { return nil } // the pinned key already authenticated the node
	res := &resumer{
		session: session, window: o.window, rec: o.rec,
		redial: func(ctx context.Context) (*websocket.Conn, *sealer, *opener, error) {
			conn, err := dialLocal(ctx, client, o, relayURL, session)
			if err != nil {
				return nil, nil, nil, err
			}
			send, recv, err := handshakeLocal(ctx, conn, session, pinned, o, admitByKey)
			if err != nil {
				conn.CloseNow()
				return nil, nil, nil, err
			}
			return conn, send, recv, nil
		},
	}
	s, err := acceptRemote(ctx, conn, session, pinned, o, admitByKey, res)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "attach failed")
		return nil, err
	}
	return s, nil
}

// awaitKey asks keyFn for the node's published key until one is present.
func awaitKey(ctx context.Context, o *options, keyFn func(context.Context) ([]byte, error)) ([]byte, error) {
	deadline := o.clock.Now().Add(EnrollTTL)
	tick, stop := o.clock.Tick(keyPollInterval)
	defer stop()
	for {
		key, err := keyFn(ctx)
		if err != nil {
			return nil, fmt.Errorf("remotechan: reading the node's published key: %w", err)
		}
		if len(key) > 0 {
			return key, nil
		}
		if !o.clock.Now().Before(deadline) {
			return nil, ErrKeyUnpublished
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// acceptRemote runs local's half of the handshake on an already-dialed
// connection: read the remote's key half, answer with ours, derive the keys
// against the pinned node key, open the remote's enrollment, then ask admit
// whether to let it in. Open admits by redeeming the enrollment's single-use
// token; AttachServerSession admits by the key the keys were already derived
// from. The handshake, the key_mismatch refusal and the refusal recorder are the
// same for both.
//
// res, when set, lets the returned Session redial after the relay drops it.
func acceptRemote(ctx context.Context, conn *websocket.Conn, session string, pinned []byte, o *options, admit func(appMsg) error, res *resumer) (*Session, error) {
	hctx, cancel := context.WithTimeout(ctx, EnrollTTL)
	defer cancel()
	send, recv, err := handshakeLocal(hctx, conn, session, pinned, o, admit)
	if err != nil {
		return nil, err
	}
	return newSession(ctx, RoleLocal, conn, send, recv, o.clock, res), nil
}

// handshakeLocal is local's half of the handshake, bounded by ctx. The remote may
// re-send its key half while it waits for ours (a redial), so a key half already
// answered is ignored, and a different one is a fresh attempt by the remote and is
// answered afresh: this end never waits on an attempt the remote has abandoned.
func handshakeLocal(ctx context.Context, conn *websocket.Conn, session string, pinned []byte, o *options, admit func(appMsg) error) (*sealer, *opener, error) {
	conn.SetReadLimit(maxFrameBytes)
	var send *sealer
	var recv *opener
	var answered []byte // the remote key half the keys were derived from
	for {
		frame, err := readFrame(ctx, conn)
		if err != nil {
			if send == nil {
				return nil, nil, fmt.Errorf("remotechan: waiting for the remote: %w", err)
			}
			return nil, nil, fmt.Errorf("remotechan: waiting for enrollment: %w", err)
		}
		if len(frame) == 1+pubLen && frame[0] == frameHelloRemote {
			if answered != nil && bytes.Equal(frame[1:], answered) {
				continue
			}
			eL, err := newEphemeral()
			if err != nil {
				return nil, nil, err
			}
			if send, recv, err = localKeys(session, eL, frame[1:], pinned); err != nil {
				return nil, nil, err
			}
			if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{frameHelloLocal}, eL.PublicKey().Bytes()...)); err != nil {
				return nil, nil, err
			}
			answered = frame[1:]
			continue
		}
		if send == nil {
			return nil, nil, fmt.Errorf("%w: expected the remote's key half", ErrFrame)
		}

		plain, err := recv.open(frame)
		if errors.Is(err, errOpen) {
			return nil, nil, o.refused(ctx, session, "key_mismatch", ErrKeyMismatch)
		}
		if err != nil {
			return nil, nil, err
		}
		var m appMsg
		if json.Unmarshal(plain, &m) != nil || m.T != msgEnroll {
			return nil, nil, fmt.Errorf("%w: expected enrollment", ErrFrame)
		}

		if rErr := admit(m); rErr != nil {
			reply, _ := json.Marshal(appMsg{T: msgRefuse, Reason: rErr.Error()})
			_ = conn.Write(ctx, websocket.MessageBinary, send.seal(reply))
			return nil, nil, o.refused(ctx, session, tokenReason(rErr), rErr)
		}
		admitted, _ := json.Marshal(appMsg{T: msgAdmit})
		if err := conn.Write(ctx, websocket.MessageBinary, send.seal(admitted)); err != nil {
			return nil, nil, err
		}
		return send, recv, nil
	}
}

func tokenReason(err error) string {
	switch {
	case errors.Is(err, ErrTokenUsed):
		return "token_used"
	case errors.Is(err, ErrTokenExpired):
		return "token_expired"
	default:
		return "token_unknown"
	}
}

// refused records one refused enrollment and returns cause, joined with the
// recording failure if the ledger could not be written.
func (o *options) refused(ctx context.Context, session, reason string, cause error) error {
	if o.rec == nil {
		return cause
	}
	payload, _ := json.Marshal(map[string]string{"session": session, "reason": reason})
	err := o.rec.Record(ctx, Record{
		Kind: KindRemoteEnrollRefused, Actor: "remotechan", Body: reason, Payload: payload,
	}, o.clock.Now())
	if err != nil {
		return errors.Join(cause, fmt.Errorf("remotechan: record refused enrollment: %w", err))
	}
	return cause
}

// Join is the remote's end: dial the relay out with the node token as the
// bearer, complete the handshake with the node's own identity, and enroll with
// the same token. It returns an admitted Session, ErrEnrollRefused if local
// satelle refused the token, a *RelayStatusError (401 for a token the relay does
// not accept) if the relay refused the upgrade, or an error if the channel closed
// first (as it does for a key local did not pin).
//
// The session resumes: when the relay drops the connection in a way a restart
// explains, the node redials the same session with the same identity and token,
// for at most the resume window, and re-sends its key half until local answers, so
// the handshake completes whichever end the relay admits first.
func Join(ctx context.Context, relayURL, session string, id *Identity, token string, opts ...Option) (*Session, error) {
	o := newOptions(opts)
	conn, err := dialRemote(ctx, o, relayURL, session, token)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, EnrollTTL)
	defer cancel()
	send, recv, err := joinHandshake(hctx, conn, session, id, token, o, 0)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "join failed")
		return nil, err
	}
	res := &resumer{
		session: session, window: o.window, rec: o.rec,
		redial: func(ctx context.Context) (*websocket.Conn, *sealer, *opener, error) {
			conn, err := dialRemote(ctx, o, relayURL, session, token)
			if err != nil {
				return nil, nil, nil, err
			}
			send, recv, err := joinHandshake(ctx, conn, session, id, token, o, HelloResend)
			if err != nil {
				conn.CloseNow()
				return nil, nil, nil, err
			}
			return conn, send, recv, nil
		},
	}
	return newSession(ctx, RoleRemote, conn, send, recv, o.clock, res), nil
}

// dialRemote dials the relay as the remote with the node token as the bearer.
func dialRemote(ctx context.Context, o *options, relayURL, session, token string) (*websocket.Conn, error) {
	conn, resp, err := dialRelay(ctx, o.http, relayURL, session, RoleRemote, token)
	if err != nil {
		if resp != nil {
			return nil, &RelayStatusError{Status: resp.StatusCode, Body: string(hosted.BoundAuthBody(resp.Body))}
		}
		return nil, fmt.Errorf("remotechan: dial relay: %w", err)
	}
	return conn, nil
}

// joinHandshake is the remote's half of the handshake, bounded by ctx. With
// resend > 0 it re-sends the same key half every resend on the options clock
// until local's arrives: a frame sent while the relay has no local end may be
// dropped, so the remote asks again rather than wait on one that was lost.
func joinHandshake(ctx context.Context, conn *websocket.Conn, session string, id *Identity, token string, o *options, resend time.Duration) (*sealer, *opener, error) {
	conn.SetReadLimit(maxFrameBytes)

	eR, err := newEphemeral()
	if err != nil {
		return nil, nil, err
	}
	hello := append([]byte{frameHelloRemote}, eR.PublicKey().Bytes()...)
	if err := conn.Write(ctx, websocket.MessageBinary, hello); err != nil {
		return nil, nil, err
	}
	frame, err := awaitLocalHello(ctx, conn, o.clock, hello, resend)
	if err != nil {
		return nil, nil, fmt.Errorf("remotechan: waiting for local satelle: %w", err)
	}
	if len(frame) != 1+pubLen || frame[0] != frameHelloLocal {
		return nil, nil, fmt.Errorf("%w: expected local's key half", ErrFrame)
	}
	send, recv, err := remoteKeys(session, id, eR, frame[1:])
	if err != nil {
		return nil, nil, err
	}
	enroll, _ := json.Marshal(appMsg{T: msgEnroll, Token: token})
	if err := conn.Write(ctx, websocket.MessageBinary, send.seal(enroll)); err != nil {
		return nil, nil, err
	}

	reply, err := readFrame(ctx, conn)
	if err != nil {
		return nil, nil, fmt.Errorf("remotechan: channel closed before admission: %w", err)
	}
	plain, err := recv.open(reply)
	if err != nil {
		return nil, nil, err
	}
	var m appMsg
	if json.Unmarshal(plain, &m) != nil {
		return nil, nil, fmt.Errorf("%w: malformed reply", ErrFrame)
	}
	switch m.T {
	case msgAdmit:
		return send, recv, nil
	case msgRefuse:
		return nil, nil, fmt.Errorf("%w: %s", ErrEnrollRefused, m.Reason)
	}
	return nil, nil, fmt.Errorf("%w: expected admission", ErrFrame)
}

// awaitLocalHello reads local's key half, re-writing hello every resend (when
// resend > 0) until it arrives.
func awaitLocalHello(ctx context.Context, conn *websocket.Conn, clock Clock, hello []byte, resend time.Duration) ([]byte, error) {
	if resend <= 0 {
		return readFrame(ctx, conn)
	}
	type read struct {
		frame []byte
		err   error
	}
	got := make(chan read, 1)
	go func() {
		f, err := readFrame(ctx, conn)
		got <- read{f, err}
	}()
	tick, stop := clock.Tick(resend)
	defer stop()
	for {
		select {
		case r := <-got:
			return r.frame, r.err
		case <-tick:
			if err := conn.Write(ctx, websocket.MessageBinary, hello); err != nil {
				conn.CloseNow()
				<-got // the read ends with the connection
				return nil, err
			}
		}
	}
}

// dialRelay opens the WebSocket to <relayURL>/<session>?role=<role>. A bearer
// ("" for none) rides in the upgrade request. When the relay answers the
// upgrade with a non-101 status the response is returned beside the error.
func dialRelay(ctx context.Context, httpc *http.Client, relayURL, session string, role Role, bearer string) (*websocket.Conn, *http.Response, error) {
	u, err := url.JoinPath(relayURL, session)
	if err != nil {
		return nil, nil, err
	}
	d := &websocket.DialOptions{HTTPClient: httpc}
	if bearer != "" {
		d.HTTPHeader = http.Header{"Authorization": {"Bearer " + bearer}}
	}
	return websocket.Dial(ctx, u+"?role="+role.String(), d)
}

func readFrame(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, fmt.Errorf("%w: text message on the channel", ErrFrame)
	}
	return data, nil
}

// newSessionID is the 128-bit random name the relay pairs the two ends under.
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("remotechan: session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
