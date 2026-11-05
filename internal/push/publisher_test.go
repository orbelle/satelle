package push_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/push"
)

// roundTripFunc is an http.RoundTripper made of a function, so a test sees every
// request the publisher tries to send, to any address.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublisherInactiveNoNetwork(t *testing.T) {
	var hits atomic.Int32
	counting := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, errors.New("unexpected request from an inert publisher")
	})}

	// Empty endpoint: Notify must not send anything. The inert path returns before
	// any goroutine exists, so there is no asynchronous work to wait out — a
	// request, had one been attempted, would already be counted when Notify returns.
	p := &push.Publisher{Endpoint: "", RepoKey: "repo-x", Client: counting, OnPost: func(*http.Request) { hits.Add(1) }}
	p.Notify("stories")
	if hits.Load() != 0 {
		t.Fatalf("inert publisher made %d network calls", hits.Load())
	}

	// Nil receiver: also inert.
	var nilP *push.Publisher
	nilP.Notify("stories")
	if hits.Load() != 0 {
		t.Fatalf("nil publisher made network calls")
	}
}

func TestPublisherPostsEvent(t *testing.T) {
	var got push.Event
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if r.Method != http.MethodPost || r.URL.Path != "/ingest/change" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("json: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	p := &push.Publisher{Endpoint: srv.URL, RepoKey: "satelle-deadbeef"}
	p.Notify("stories")
	select {
	case <-done:
	case <-time.After(10*time.Second):
		t.Fatal("timeout waiting for POST")
	}
	if got.RepoKey != "satelle-deadbeef" || got.Topic != "stories" || got.Entity != "story" {
		t.Fatalf("event = %+v", got)
	}
	if got.At == "" {
		t.Error("missing at")
	}
}

func TestPublisherFailSilentUnreachable(t *testing.T) {
	// Black-hole: the transport parks the request until the test releases it, then
	// fails it as an unreachable endpoint would. Notify must return while the
	// attempt is still parked (fire-and-forget), and the goroutine must end
	// without affecting the caller once the attempt fails.
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	p := &push.Publisher{
		Endpoint: "http://127.0.0.1:1",
		RepoKey:  "repo",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			close(entered)
			<-release
			defer close(finished)
			return nil, errors.New("connect: connection refused")
		})},
	}

	returned := make(chan struct{})
	go func() {
		p.Notify("tasks")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10*time.Second):
		close(release)
		t.Fatal("Notify blocked on the HTTP attempt — must be fire-and-forget")
	}
	select {
	case <-entered:
	case <-time.After(10*time.Second):
		close(release)
		t.Fatal("the publisher never attempted the POST")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(10*time.Second):
		t.Fatal("the failed attempt never completed")
	}
}
