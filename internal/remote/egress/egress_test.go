package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type backend struct {
	srv  *httptest.Server
	hits atomic.Int32
	last atomic.Value
}

func newBackend(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		b.last.Store(r.Clone(context.Background()))
		h(w, r)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) authority() string { return strings.TrimPrefix(b.srv.URL, "http://") }

func (b *backend) lastRequest() *http.Request {
	r, _ := b.last.Load().(*http.Request)
	return r
}

type fixture struct {
	proxy *httptest.Server
	log   *syncBuf
	dials atomic.Int32
}

func newFixture(t *testing.T, allow ...string) *fixture {
	t.Helper()
	return newFixtureDial(t, (&net.Dialer{}).DialContext, allow...)
}

func newFixtureDial(t *testing.T, dial dialFunc, allow ...string) *fixture {
	t.Helper()
	f := &fixture{log: &syncBuf{}}
	p := newProxy(remotedef.Network{Allow: allow}, f.log, func(ctx context.Context, network, addr string) (net.Conn, error) {
		f.dials.Add(1)
		return dial(ctx, network, addr)
	})
	f.proxy = httptest.NewServer(p)
	t.Cleanup(f.proxy.Close)
	return f
}

func (f *fixture) raw(t *testing.T, request string) (*http.Response, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(f.proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("reading the proxy's answer to %q: %v", request, err)
	}
	return resp, c
}

func (f *fixture) client() *http.Client {
	u, _ := url.Parse(f.proxy.URL)
	return &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func hostOf(authority string) string {
	h, _, _ := net.SplitHostPort(authority)
	return h
}

func TestProxyForwardsOnlyPermittedHosts(t *testing.T) {
	ok := newBackend(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "allowed") })
	f := newFixture(t, hostOf(ok.authority()))
	resp, err := f.client().Get(ok.srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "allowed" {
		t.Fatalf("GET through the proxy = %d %q", resp.StatusCode, body)
	}
	resp, err = f.client().Get("http://not-allowed.invalid:8080/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "not-allowed.invalid is not in the definition's [network] allow") {
		t.Fatalf("GET of a host off the list = %d %q", resp.StatusCode, body)
	}
	if !strings.Contains(f.log.String(), "refused GET not-allowed.invalid:8080") {
		t.Fatalf("the refusal was not logged: %q", f.log.String())
	}
}

func TestProxyConnectTunnelsOnlyPermittedHosts(t *testing.T) {
	ok := newBackend(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "tunnelled") })
	f := newFixture(t, hostOf(ok.authority()))
	resp, c := f.raw(t, "CONNECT "+ok.authority()+" HTTP/1.1\r\nHost: "+ok.authority()+"\r\n\r\n")
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+ok.authority()+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	if !strings.Contains(string(got), "tunnelled") {
		t.Fatalf("the tunnel carried %q", got)
	}
	resp, _ = f.raw(t, "CONNECT other.invalid:443 HTTP/1.1\r\nHost: other.invalid:443\r\n\r\n")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT to a host off the list = %d", resp.StatusCode)
	}
	if !strings.Contains(f.log.String(), "refused CONNECT other.invalid:443") {
		t.Fatalf("the refusal was not logged: %q", f.log.String())
	}
}

func TestProxyWildcardEntry(t *testing.T) {
	var dialled []string
	var mu sync.Mutex
	f := newFixtureDial(t, func(_ context.Context, _, addr string) (net.Conn, error) {
		mu.Lock()
		dialled = append(dialled, addr)
		mu.Unlock()
		return nil, errors.New("no network in this test")
	}, "*.example.org")
	resp, _ := f.raw(t, "CONNECT a.example.org:443 HTTP/1.1\r\nHost: a.example.org:443\r\n\r\n")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("CONNECT a.example.org = %d, want the permitted dial's 502", resp.StatusCode)
	}
	resp, _ = f.raw(t, "CONNECT example.org:443 HTTP/1.1\r\nHost: example.org:443\r\n\r\n")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CONNECT example.org = %d, want 403", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialled) != 1 || dialled[0] != "a.example.org:443" {
		t.Fatalf("dialled %q", dialled)
	}
}

func TestProxyDoesNotFollowRedirects(t *testing.T) {
	off := newBackend(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "reached") })
	ok := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+off.authority()+"/", http.StatusFound)
	})
	f := newFixture(t, "localhost")
	resp, err := f.client().Get("http://localhost:" + portOf(ok.authority()) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), off.authority()) {
		t.Fatalf("redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if off.hits.Load() != 0 {
		t.Fatal("the proxy followed the redirect to the other backend")
	}
}

func TestProxyTakesTheAuthorityFromTheTarget(t *testing.T) {
	ok := newBackend(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	f := newFixture(t, hostOf(ok.authority()))
	resp, _ := f.raw(t, "GET http://other.invalid:"+portOf(ok.authority())+"/ HTTP/1.1\r\nHost: "+ok.authority()+"\r\n\r\n")
	if resp.StatusCode != http.StatusForbidden || ok.hits.Load() != 0 || f.dials.Load() != 0 {
		t.Fatalf("a disallowed target with a permitted Host = %d, backend hits %d, dials %d", resp.StatusCode, ok.hits.Load(), f.dials.Load())
	}
	resp, _ = f.raw(t, "GET http://"+ok.authority()+"/ HTTP/1.1\r\nHost: other.invalid\r\n\r\n")
	if resp.StatusCode != 200 || ok.hits.Load() != 1 {
		t.Fatalf("a permitted target with a disallowed Host = %d, backend hits %d", resp.StatusCode, ok.hits.Load())
	}
	if got := ok.lastRequest().Host; got != ok.authority() {
		t.Fatalf("the backend saw Host %q, want %q", got, ok.authority())
	}
}

func portOf(authority string) string {
	_, p, _ := net.SplitHostPort(authority)
	return p
}

func TestProxyStrictConnect(t *testing.T) {
	f := newFixture(t, "permitted.example")
	for _, target := range []string{
		"permitted.example",
		"permitted.example:443@evil.example:443",
		":443",
		"permitted.example:99999",
		"permitted.example:0",
		"permitted.example:+443",
	} {
		c, err := net.Dial("tcp", strings.TrimPrefix(f.proxy.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: permitted.example:443\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		c.Close()
		if err != nil {
			t.Fatalf("CONNECT %s: %v", target, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("CONNECT %s = %d, want 400", target, resp.StatusCode)
		}
	}
	if n := f.dials.Load(); n != 0 {
		t.Fatalf("a malformed CONNECT dialled %d time(s)", n)
	}
}

func TestProxyRefusesOtherRequestForms(t *testing.T) {
	ok := newBackend(t, func(w http.ResponseWriter, _ *http.Request) {})
	f := newFixture(t, hostOf(ok.authority()))
	for _, req := range []string{
		"GET / HTTP/1.1\r\nHost: " + ok.authority() + "\r\n\r\n",
		"GET https://" + ok.authority() + "/ HTTP/1.1\r\nHost: " + ok.authority() + "\r\n\r\n",
	} {
		resp, _ := f.raw(t, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", strings.SplitN(req, "\r\n", 2)[0], resp.StatusCode)
		}
	}
	if ok.hits.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("a refused request form reached the backend")
	}
}

func TestProxyStripsProxyHeaders(t *testing.T) {
	ok := newBackend(t, func(w http.ResponseWriter, _ *http.Request) {})
	f := newFixture(t, hostOf(ok.authority()))
	resp, _ := f.raw(t, "GET http://"+ok.authority()+"/ HTTP/1.1\r\nHost: "+ok.authority()+
		"\r\nProxy-Authorization: Basic c2VjcmV0\r\nProxy-Connection: keep-alive\r\nConnection: X-Drop\r\nX-Drop: 1\r\nX-Keep: 1\r\n\r\n")
	if resp.StatusCode != 200 {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	h := ok.lastRequest().Header
	for _, name := range []string{"Proxy-Authorization", "Proxy-Connection", "X-Drop"} {
		if h.Get(name) != "" {
			t.Errorf("%s reached the backend: %q", name, h.Get(name))
		}
	}
	if h.Get("X-Keep") != "1" {
		t.Error("an end-to-end header was dropped")
	}
}

func TestListenAddr(t *testing.T) {
	inet := func(cidr string) net.Addr {
		ip, n, _ := net.ParseCIDR(cidr)
		return &net.IPNet{IP: ip, Mask: n.Mask}
	}
	lo, bridge, internal := inet("127.0.0.1/8"), inet("172.17.0.5/16"), inet("172.30.0.2/16")
	got, err := ListenAddr([]net.Addr{lo, bridge, internal}, "172.30.0.0/16", 3128)
	if err != nil || got != "172.30.0.2:3128" {
		t.Fatalf("ListenAddr = %q, %v", got, err)
	}
	if _, err := ListenAddr([]net.Addr{lo, bridge}, "172.30.0.0/16", 3128); err == nil || !strings.Contains(err.Error(), "172.30.0.0/16") {
		t.Fatalf("no address in the subnet = %v", err)
	}
	if _, err := ListenAddr([]net.Addr{internal, inet("172.30.0.3/16")}, "172.30.0.0/16", 3128); err == nil {
		t.Fatal("two addresses in the subnet were accepted")
	}
	got, err = ListenAddr([]net.Addr{inet("0.0.0.0/0"), lo}, "0.0.0.0/0", 3128)
	if strings.HasPrefix(got, "0.0.0.0") {
		t.Fatalf("ListenAddr bound every interface: %q, %v", got, err)
	}
	if _, err := ListenAddr([]net.Addr{internal}, "not-a-cidr", 3128); err == nil {
		t.Fatal("a malformed subnet was accepted")
	}
}

func TestProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := Probe(addr); err != nil {
		t.Fatalf("Probe of a listener = %v", err)
	}
	ln.Close()
	if err := Probe(addr); err == nil {
		t.Fatal("Probe of a closed listener succeeded")
	}
}
