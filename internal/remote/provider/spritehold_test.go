package provider

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AC1: on a sprite the hold PUTs a task with an expiry when the job starts,
// renews it at less than half the expiry, and DELETEs it when released, after
// which nothing more is sent.
func TestSpriteHoldPutsRenewsAndDeletes(t *testing.T) {
	api := NewSpriteAPI(t)
	var out bytes.Buffer
	const expiry, every = 300 * time.Millisecond, 50 * time.Millisecond
	release := SpriteHold{Socket: api.Socket, Name: "satelle-nod_A1", Expiry: expiry, Every: every, Out: &out}.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	release()
	release() // idempotent
	n := len(api.Calls())
	time.Sleep(120 * time.Millisecond)

	calls := api.Calls()
	if len(calls) != n {
		t.Errorf("calls grew from %d to %d after release", n, len(calls))
	}
	const path = "/v1/tasks/satelle-nod-a1"
	if len(calls) < 4 {
		t.Fatalf("calls = %+v, want a PUT, at least 2 renewals and a DELETE", calls)
	}
	puts := calls[:len(calls)-1]
	for i, c := range puts {
		if c.Method != http.MethodPut || c.Path != path || c.Body != `{"expire":"300ms"}` {
			t.Errorf("call %d = %s %s %s, want PUT %s with the expiry", i, c.Method, c.Path, c.Body, path)
		}
		if i > 0 {
			if gap := c.At.Sub(puts[i-1].At); gap >= expiry/2 {
				t.Errorf("renewal %d came %v after the last, not under half the expiry %v", i, gap, expiry)
			}
		}
	}
	if last := calls[len(calls)-1]; last.Method != http.MethodDelete || last.Path != path {
		t.Errorf("last call = %s %s, want DELETE %s", last.Method, last.Path, path)
	}
	if out.Len() != 0 {
		t.Errorf("the hold logged %q, want nothing when every call succeeds", out.String())
	}
}

// AC1: a renewal interval at or past half the expiry is pulled inside it, and a
// whole-minute expiry is sent in the Tasks API's minute form.
func TestSpriteHoldClampsRenewalInsideHalfExpiry(t *testing.T) {
	api := NewSpriteAPI(t)
	const expiry = 240 * time.Millisecond
	release := SpriteHold{Socket: api.Socket, Name: "x", Expiry: expiry, Every: time.Hour}.Start(context.Background())
	time.Sleep(250 * time.Millisecond)
	release()
	if puts := len(api.Calls()) - 1; puts < 3 {
		t.Errorf("PUTs = %d in more than the expiry, want renewals inside half of it", puts)
	}
	if got := spriteDuration(5 * time.Minute); got != "5m" {
		t.Errorf("spriteDuration(5m) = %q, want 5m", got)
	}
}

// AC2: with no sprite API socket the hold does nothing: no call, no log, and a
// release that returns at once.
func TestSpriteHoldWithoutSocketIsANoOp(t *testing.T) {
	var out bytes.Buffer
	release := SpriteHold{Socket: filepath.Join(t.TempDir(), "absent.sock"), Name: "n", Out: &out}.Start(context.Background())
	done := make(chan struct{})
	go func() { release(); release(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release of a no-op hold did not return")
	}
	if out.Len() != 0 {
		t.Errorf("a node with no sprite socket logged %q", out.String())
	}
}

// A refused hold is logged and never fails the job.
func TestSpriteHoldLogsARefusedCall(t *testing.T) {
	api := NewSpriteAPI(t)
	api.Refuse(http.StatusInternalServerError)
	var out bytes.Buffer
	SpriteHold{Socket: api.Socket, Name: "n", Out: &out}.Start(context.Background())()
	if got := out.String(); !strings.Contains(got, "node: sprite hold: PUT") || !strings.Contains(got, "node: sprite hold: DELETE") {
		t.Errorf("log = %q, want the refused PUT and DELETE named", got)
	}
}
