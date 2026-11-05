package remotechan

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// rig is a fake satelle-server with a signed-in local client and one node.
type rig struct {
	srv         *fakeserver.Server
	local, node *hosted.Client
	session     string
	id          *Identity
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv := fakeserver.Start(t, "user-tok")
	srv.AddNode(&fakeserver.Node{ID: "nod_1", Token: "node-tok"})
	dial := func(tok string) *hosted.Client {
		c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: tok}, hosted.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return &rig{srv: srv, local: dial("user-tok"), node: dial("node-tok"), session: "nod_1", id: id}
}

func keyOf(pub []byte) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return pub, nil }
}

type joined struct {
	s   *Session
	err error
}

// pair attaches local and joins the node concurrently.
func (r *rig) pair(t *testing.T, pinned []byte, opts ...Option) (*Session, *Session, error, error) {
	t.Helper()
	ctx := testCtx(t)
	ch := make(chan joined, 1)
	go func() {
		s, err := Join(ctx, r.node.Relay, r.session, r.id, opts...)
		ch <- joined{s, err}
	}()
	local, lerr := Attach(ctx, r.local.Relay, r.session, keyOf(pinned), opts...)
	j := <-ch
	for _, s := range []*Session{local, j.s} {
		if s != nil {
			t.Cleanup(func() { _ = s.Close() })
		}
	}
	return local, j.s, lerr, j.err
}

type memRecorder struct {
	mu   sync.Mutex
	recs []Record
}

func (m *memRecorder) Record(_ context.Context, r Record, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func (m *memRecorder) count(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.recs {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

func exchange(t *testing.T, local, remote *Session, msg string) {
	t.Helper()
	ctx := testCtx(t)
	if err := local.Send(ctx, "j1", []byte(msg+" to node")); err != nil {
		t.Fatal(err)
	}
	if m, err := remote.Recv(ctx); err != nil || string(m.Body) != msg+" to node" || m.Job != "j1" {
		t.Fatalf("remote got %+v, %v", m, err)
	}
	if err := remote.Send(ctx, "j1", []byte(msg+" to local")); err != nil {
		t.Fatal(err)
	}
	if m, err := local.Recv(ctx); err != nil || string(m.Body) != msg+" to local" {
		t.Fatalf("local got %+v, %v", m, err)
	}
}

// The two ends pair through RelayService/Connect, and the relay forwards only
// sealed bytes.
func TestPairOverGRPCRelay(t *testing.T) {
	r := newRig(t)
	local, remote, lerr, rerr := r.pair(t, r.id.Public())
	if lerr != nil || rerr != nil {
		t.Fatalf("attach %v, join %v", lerr, rerr)
	}
	exchange(t, local, remote, "secret-payload")
	frames := r.srv.RelayFrames(r.session)
	if len(frames) < 4 {
		t.Fatalf("relay forwarded %d frames", len(frames))
	}
	for _, f := range frames {
		if bytes.Contains(f, []byte("secret-payload")) || bytes.Contains(f, []byte("enroll")) {
			t.Fatalf("the relay saw plaintext: %q", f)
		}
	}
	var methods []string
	for _, c := range r.srv.Calls() {
		methods = append(methods, c.Method)
	}
	if !strings.Contains(strings.Join(methods, " "), "/satelle.v1.RelayService/Connect") {
		t.Fatalf("calls = %v", methods)
	}
}

func TestSubstitutedKeyRefused(t *testing.T) {
	r := newRig(t)
	other, _ := NewIdentity()
	rec := &memRecorder{}
	_, _, lerr, rerr := r.pair(t, other.Public(), WithRecorder(rec))
	if !errors.Is(lerr, ErrKeyMismatch) || !errors.Is(rerr, ErrEnrollRefused) {
		t.Fatalf("attach = %v, join = %v; want ErrKeyMismatch, ErrEnrollRefused", lerr, rerr)
	}
	if rec.count(KindRemoteEnrollRefused) != 1 {
		t.Fatalf("refusal not recorded: %+v", rec.recs)
	}
}

// jumpClock's every reading is an hour on, and its ticks are always due.
type jumpClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *jumpClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Hour)
	return c.now
}

func (c *jumpClock) Tick(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time)
	close(ch)
	return ch, func() {}
}

func TestUnpublishedKeyRefused(t *testing.T) {
	r := newRig(t)
	rec := &memRecorder{}
	_, err := Attach(testCtx(t), r.local.Relay, r.session, keyOf(nil), WithClock(&jumpClock{}), WithRecorder(rec))
	if !errors.Is(err, ErrKeyUnpublished) || rec.count(KindRemoteEnrollRefused) != 1 {
		t.Fatalf("attach = %v, records %+v", err, rec.recs)
	}
}

func TestRefusedNodeTokenIsNotRetried(t *testing.T) {
	r := newRig(t)
	bad, err := hosted.Dial(r.srv.URL, hosted.NodeTokenStore{Server: r.srv.URL, Token: "wrong"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	_, err = Join(testCtx(t), bad.Relay, r.session, r.id)
	if status.Code(errors.Unwrap(errors.Unwrap(err))) != codes.Unauthenticated && !strings.Contains(err.Error(), "Unauthenticated") {
		t.Fatalf("join = %v, want UNAUTHENTICATED", err)
	}
	if classifyClose(status.Error(codes.Unauthenticated, "x")) != closeRefusal {
		t.Fatal("UNAUTHENTICATED is not a refusal")
	}
}

// Close tells the other end, which ends without redialling.
func TestCloseEndsTheOtherEnd(t *testing.T) {
	r := newRig(t)
	local, remote, lerr, rerr := r.pair(t, r.id.Public())
	if lerr != nil || rerr != nil {
		t.Fatalf("attach %v, join %v", lerr, rerr)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-remote.Done():
	case <-time.After(hangBound):
		t.Fatal("the remote did not end")
	}
	if !errors.Is(remote.Err(), ErrClosed) {
		t.Fatalf("remote ended with %v", remote.Err())
	}
}

// A relay restart drops both streams; both ends redial the same session,
// handshake again and carry traffic.
func TestResumeAfterRelayRestart(t *testing.T) {
	r := newRig(t)
	rec := &memRecorder{}
	local, remote, lerr, rerr := r.pair(t, r.id.Public(), WithRecorder(rec), WithResumeWindow(time.Minute))
	if lerr != nil || rerr != nil {
		t.Fatalf("attach %v, join %v", lerr, rerr)
	}
	exchange(t, local, remote, "before")
	r.srv.DropRelays()
	eventually(t, "both ends resumed", func() bool { return rec.count(KindRemoteChannelResumed) >= 2 })
	exchange(t, local, remote, "after")
}

func TestJobThatNeverBeatsIsLost(t *testing.T) {
	r := newRig(t)
	clock := newFakeClock()
	local, _, lerr, rerr := r.pair(t, r.id.Public(), WithClock(clock))
	if lerr != nil || rerr != nil {
		t.Fatalf("attach %v, join %v", lerr, rerr)
	}
	clock.waitTickers(t, 2)
	local.Expect("silent")
	for i := 0; i < int((LostAfter+HeartbeatInterval)/HeartbeatInterval); i++ {
		clock.Advance(HeartbeatInterval)
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
}

// The channel only dials out: no production file can listen.
func TestProductionCodeNeverListens(t *testing.T) {
	banned := map[string]map[string]bool{
		"net":  {"Listen": true, "ListenTCP": true, "ListenUnix": true, "ListenPacket": true},
		"http": {"ListenAndServe": true, "Serve": true},
		"grpc": {"NewServer": true},
	}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && banned[x.Name][sel.Sel.Name] {
					t.Errorf("%s: %s.%s — the channel only dials out", fset.Position(sel.Pos()), x.Name, sel.Sel.Name)
				}
			}
			return true
		})
	}
}
