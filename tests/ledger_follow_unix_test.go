//go:build integration && unix

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const followBound = 5 * time.Second

type followLoc struct {
	s   *sandbox
	ui  string
	key string
}

func newFollowLoc(t *testing.T, bind func(s *sandbox)) *followLoc {
	t.Helper()
	s := newSandboxOutsideTemp(t)
	s.env["SATELLE_TOKEN"] = devToken
	ui := startSatelled(t, s)
	s.must("init")
	bind(s)
	s.must("project", "add")
	lines := strings.Split(strings.TrimSpace(s.must("project", "partitions")), "\n")
	if len(lines) != 2 {
		t.Fatalf("partitions:\n%s", strings.Join(lines, "\n"))
	}
	return &followLoc{s: s, ui: ui, key: strings.Fields(lines[1])[0]}
}

func (l *followLoc) stories(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(l.ui + "/r/" + l.key + "/fragment/stories")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (l *followLoc) story(id string) (item.Item, bool) {
	r := l.s.run("story", "get", id, "--json")
	if r.code != 0 {
		return item.Item{}, false
	}
	var it item.Item
	if json.Unmarshal([]byte(r.out), &it) != nil {
		return item.Item{}, false
	}
	return it, true
}

func (l *followLoc) shows(t *testing.T, id, title, status string) bool {
	t.Helper()
	it, ok := l.story(id)
	if !ok || it.Title != title || (status != "" && it.Status != status) {
		return false
	}
	frag := l.stories(t)
	if !strings.Contains(frag, id) || !strings.Contains(frag, title) {
		return false
	}
	return status == "" || strings.Contains(frag, `data-status="`+status+`"`)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type followProc struct {
	cmd  *exec.Cmd
	out  *syncBuf
	done chan struct{}
}

func startFollow(t *testing.T, l *followLoc) *followProc {
	t.Helper()
	cmd := exec.Command(satelleBin, "ledger", "follow")
	cmd.Dir = l.s.dir
	cmd.Env = l.s.environ()
	p := &followProc{cmd: cmd, out: &syncBuf{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.kill()
		t.Logf("follow on %s:\n%s", l.s.dir, p.out.String())
	})
	p.await(t, "subscribed to", 15*time.Second)
	return p
}

func (p *followProc) kill() {
	_ = p.cmd.Process.Kill()
	<-p.done
}

func (p *followProc) await(t *testing.T, line string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(p.out.String(), line) {
		if time.Now().After(deadline) {
			t.Fatalf("follow did not print %q within %s:\n%s", line, within, p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func within(t *testing.T, what string, bound time.Duration, ok func() bool) {
	t.Helper()
	t0 := time.Now()
	for !ok() {
		if time.Since(t0) > bound {
			t.Fatalf("%s: not seen within %s", what, bound)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("%s: seen after %s", what, time.Since(t0).Round(time.Millisecond))
}

func followPair(t *testing.T, srv *fakeserver.Server, bServer string) (a, b *followLoc) {
	t.Helper()
	a = newFollowLoc(t, func(s *sandbox) { s.must("project", "create", "shared", "--bind", "--server", srv.URL) })
	b = newFollowLoc(t, func(s *sandbox) { s.must("project", "bind", "shared", "--server", bServer) })
	return a, b
}

func TestFollowCarriesCreateUpdateMoveWithinFiveSeconds(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	a, b := followPair(t, srv, srv.URL)
	startFollow(t, a)
	startFollow(t, b)

	id := a.s.create("story", "--title", "made on a", "--criteria", "travels").ID
	within(t, "create on B", followBound, func() bool { return b.shows(t, id, "made on a", "") })
	a.s.must("story", "set", id, "--title", "renamed on a")
	within(t, "update on B", followBound, func() bool { return b.shows(t, id, "renamed on a", "") })
	a.s.must("story", "set", id, "--status", "done")
	within(t, "move on B", followBound, func() bool { return b.shows(t, id, "renamed on a", "done") })
}

type cutProxy struct {
	t      *testing.T
	addr   string
	target string
	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]bool
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	p := &cutProxy{t: t, target: target, conns: map[net.Conn]bool{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	p.serve(ln)
	t.Cleanup(p.Cut)
	return p
}

func (p *cutProxy) URL() string { return "http://" + p.addr }

func (p *cutProxy) serve(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.pipe(c)
		}
	}()
}

func (p *cutProxy) pipe(c net.Conn) {
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		c.Close()
		return
	}
	p.mu.Lock()
	if p.ln == nil {
		p.mu.Unlock()
		c.Close()
		up.Close()
		return
	}
	p.conns[c], p.conns[up] = true, true
	p.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); up.Close(); c.Close() }()
	_, _ = io.Copy(c, up)
	c.Close()
	up.Close()
}

func (p *cutProxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		p.ln.Close()
		p.ln = nil
	}
	for c := range p.conns {
		c.Close()
		delete(p.conns, c)
	}
}

func (p *cutProxy) Restore() {
	p.t.Helper()
	var ln net.Listener
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		ln, err = net.Listen("tcp", p.addr)
		if err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		p.t.Fatalf("proxy cannot listen again on %s: %v", p.addr, err)
	}
	p.serve(ln)
}

func TestFollowCatchesUpAfterOutage(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	proxy := newCutProxy(t, strings.TrimPrefix(srv.URL, "http://"))
	a, b := followPair(t, srv, proxy.URL())
	startFollow(t, a)
	fb := startFollow(t, b)

	proxy.Cut()
	fb.await(t, "subscription dropped", 10*time.Second)

	id := a.s.create("story", "--title", "made while b was off", "--criteria", "catches up").ID
	a.s.must("story", "set", id, "--title", "renamed while b was off")
	a.s.must("story", "set", id, "--status", "done")
	localEvents := 0
	var evs []struct {
		Item string `json:"item"`
	}
	a.s.mustJSON(&evs, "ledger", "list")
	for _, e := range evs {
		if e.Item == id {
			localEvents++
		}
	}
	within(t, "A's follower pushes every event of "+id, followBound, func() bool { return len(srv.Events("shared", id)) == localEvents })

	time.Sleep(time.Second)
	if _, ok := b.story(id); ok {
		t.Fatalf("B holds %s while its link to the store is cut", id)
	}
	if strings.Contains(b.stories(t), id) {
		t.Fatalf("B's satelled shows %s while its link to the store is cut", id)
	}

	proxy.Restore()
	within(t, "B catches up after the outage", followBound, func() bool {
		return b.shows(t, id, "renamed while b was off", "done")
	})
}

func TestTimerPassRecoversWhenFollowIsDown(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	a, b := followPair(t, srv, srv.URL)
	startFollow(t, a)
	fb := startFollow(t, b)

	fb.kill()

	y := a.s.create("story", "--title", "made on a while b's follow is down", "--criteria", "pulled by the timer").ID
	within(t, "A's follower pushes "+y, followBound, func() bool { return len(srv.Events("shared", y)) > 0 })
	z := b.s.create("story", "--title", "made on b while its follow is down", "--criteria", "pushed by the timer").ID
	time.Sleep(time.Second)
	if n := len(srv.Events("shared", z)); n != 0 {
		t.Fatalf("satelle.dev holds %d event(s) of %s though nothing ran on B", n, z)
	}
	if _, ok := b.story(y); ok {
		t.Fatalf("B holds %s though its follow is down", y)
	}

	r := b.s.run("ledger", "autosync")
	if r.code != 0 {
		t.Fatalf("ledger autosync on B: exit %d\n%s%s", r.code, r.out, r.err)
	}
	var list []item.Item
	b.s.mustJSON(&list, "story", "list")
	found := false
	for _, it := range list {
		found = found || it.ID == y
	}
	if !found {
		t.Errorf("B's story list lacks %s after the timer pass", y)
	}
	if len(srv.Events("shared", z)) == 0 {
		t.Errorf("satelle.dev lacks %s after the timer pass", z)
	}
	if frag := b.stories(t); !strings.Contains(frag, y) {
		t.Errorf("B's satelled lacks %s after the timer pass:\n%s", y, frag)
	}
}
