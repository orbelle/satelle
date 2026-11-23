package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const dialTimeout = 10 * time.Second

var hopHeaders = []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type proxy struct {
	allow remotedef.Network
	dial  dialFunc
	mu    sync.Mutex
	log   io.Writer
}

func Handler(n remotedef.Network, log io.Writer) http.Handler {
	return newProxy(n, log, (&net.Dialer{Timeout: dialTimeout}).DialContext)
}

func newProxy(n remotedef.Network, log io.Writer, dial dialFunc) *proxy {
	if log == nil {
		log = io.Discard
	}
	return &proxy{allow: n, dial: dial, log: log}
}

func (p *proxy) refuse(w http.ResponseWriter, r *http.Request, code int, authority, msg string) {
	p.mu.Lock()
	fmt.Fprintf(p.log, "satelle egress: refused %s %s: %s\n", r.Method, authority, msg)
	p.mu.Unlock()
	http.Error(w, "satelle egress: "+msg, code)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

func checkAuthority(host, port string) error {
	switch {
	case host == "":
		return errors.New("no host")
	case strings.ContainsAny(host, "@/\\% \t\r\n"):
		return fmt.Errorf("host %q is not a host", host)
	}
	if port == "" || strings.TrimLeft(port, "0123456789") != "" {
		return fmt.Errorf("port %q is not a port", port)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q is not a port", port)
	}
	return nil
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Host
	if r.URL.User != nil || r.URL.Path != "" || r.URL.RawQuery != "" || r.URL.Opaque != "" {
		p.refuse(w, r, http.StatusBadRequest, target, "the CONNECT target is not host:port")
		return
	}
	host, port, err := net.SplitHostPort(target)
	if err == nil {
		err = checkAuthority(host, port)
	}
	if err != nil {
		p.refuse(w, r, http.StatusBadRequest, target, "the CONNECT target is not host:port")
		return
	}
	if !p.allow.Permits(host) {
		p.refuse(w, r, http.StatusForbidden, target, host+" is not in the definition's [network] allow")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "satelle egress: the connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}
	authority := net.JoinHostPort(host, port)
	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	upstream, err := p.dial(ctx, "tcp", authority)
	cancel()
	if err != nil {
		http.Error(w, "satelle egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	client, brw, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	tunnel(client, brw.Reader, upstream)
}

func tunnel(client net.Conn, buffered *bufio.Reader, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, buffered)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
	client.Close()
	upstream.Close()
	<-done
}

func stripHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

func (p *proxy) forward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() || r.URL.Scheme != "http" || r.URL.User != nil {
		p.refuse(w, r, http.StatusBadRequest, r.URL.Host, "only an absolute http:// request or a CONNECT is proxied")
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
	}
	if err := checkAuthority(host, port); err != nil {
		p.refuse(w, r, http.StatusBadRequest, r.URL.Host, err.Error())
		return
	}
	authority := net.JoinHostPort(host, port)
	if !p.allow.Permits(host) {
		p.refuse(w, r, http.StatusForbidden, authority, host+" is not in the definition's [network] allow")
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Host = authority
	out.Host = ""
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != authority {
				return nil, fmt.Errorf("satelle egress: %s is not the checked authority %s", addr, authority)
			}
			ctx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			return p.dial(ctx, network, addr)
		},
	}
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "satelle egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func ListenAddr(addrs []net.Addr, cidr string, port int) (string, error) {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil || subnet.IP.To4() == nil {
		return "", fmt.Errorf("subnet %q is not an IPv4 CIDR", cidr)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("port %d is not a port", port)
	}
	var found []net.IP
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && subnet.Contains(ip4) {
			found = append(found, ip4)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no address of this machine is in the subnet %s", cidr)
	case 1:
		return net.JoinHostPort(found[0].String(), strconv.Itoa(port)), nil
	}
	return "", fmt.Errorf("%d addresses of this machine are in the subnet %s: the proxy binds one", len(found), cidr)
}

func Probe(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

func Serve(ctx context.Context, addr string, n remotedef.Network, log io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(n, log), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
