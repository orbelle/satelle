package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// SpriteAPISocketPath is the fly-sprite's in-sprite API socket. Its presence is
// what says the node runs on a sprite; a var so tests can point it at a fake.
var SpriteAPISocketPath = "/.sprite/api.sock"

// Sprite hold defaults: the task expires 5m after its last renewal and is
// renewed every 20s, so a stalled renewal still leaves minutes of grace.
const (
	defaultSpriteHoldExpiry = 5 * time.Minute
	defaultSpriteHoldEvery  = 20 * time.Second
	spriteHoldTimeout       = 10 * time.Second
)

// SpriteHold keeps a fly-sprite awake for as long as a run or job is in flight.
// A sprite counts only exec sessions and inbound requests as activity, not the
// node's own outbound connections, so a node whose executor is busy for minutes
// with no inbound traffic would be paused mid-run. The hold is a sprite Tasks API
// task (PUT /v1/tasks/<name> with an expiry), renewed well inside its expiry and
// deleted when the job ends. Off a sprite (no socket) it is a no-op.
type SpriteHold struct {
	Socket string        // the API socket; empty is SpriteAPISocketPath
	Name   string        // the task name; sanitised to [a-z0-9-]
	Expiry time.Duration // the task's expiry; 0 is 5m
	Every  time.Duration // the renewal interval; 0 is 20s, and always under Expiry/2
	Out    io.Writer     // where a failed PUT or DELETE is logged; nil is silent
}

// Start takes the hold and renews it until release, which deletes it. A failed
// call is logged and never fails the job. release is safe to call more than once.
func (h SpriteHold) Start(ctx context.Context) (release func()) {
	socket := h.Socket
	if socket == "" {
		socket = SpriteAPISocketPath
	}
	if _, err := os.Stat(socket); err != nil {
		return func() {}
	}
	expiry, every := h.Expiry, h.Every
	if expiry <= 0 {
		expiry = defaultSpriteHoldExpiry
	}
	if every <= 0 {
		every = defaultSpriteHoldEvery
	}
	if every >= expiry/2 {
		every = expiry / 3
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	url := "http://sprite/v1/tasks/" + spriteTaskName(h.Name)
	body := fmt.Sprintf(`{"expire":%q}`, spriteDuration(expiry))
	call := func(ctx context.Context, method string) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spriteHoldTimeout)
		defer cancel()
		var rd io.Reader
		if method == http.MethodPut {
			rd = bytes.NewReader([]byte(body))
		}
		req, err := http.NewRequestWithContext(cctx, method, url, rd)
		if err == nil {
			if rd != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			var resp *http.Response
			if resp, err = client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("%s", resp.Status)
				}
			}
		}
		if err != nil && h.Out != nil {
			fmt.Fprintf(h.Out, "node: sprite hold: %s %s: %v\n", method, url, err)
		}
	}

	call(ctx, http.MethodPut)
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				call(ctx, http.MethodPut)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			wg.Wait()
			call(ctx, http.MethodDelete)
			client.CloseIdleConnections()
		})
	}
}

// spriteTaskName is name reduced to the Tasks API's [a-z0-9-], defaulting to
// "satelle".
func spriteTaskName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if s := strings.Trim(b.String(), "-"); s != "" {
		return s
	}
	return "satelle"
}

// spriteDuration is d in the Tasks API's expiry form: whole minutes as "5m",
// otherwise Go's duration string ("300ms", "90s").
func spriteDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
