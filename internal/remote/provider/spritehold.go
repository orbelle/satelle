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

var SpriteAPISocketPath = "/.sprite/api.sock"

const (
	defaultSpriteHoldExpiry = 5 * time.Minute
	defaultSpriteHoldEvery  = 20 * time.Second
	spriteHoldTimeout       = 10 * time.Second
)

type SpriteHold struct {
	Socket string
	Name   string
	Expiry time.Duration
	Every  time.Duration
	Out    io.Writer
}

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

func spriteDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
