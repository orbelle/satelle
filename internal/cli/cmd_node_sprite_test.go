//go:build !windows

package cli

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// onSprite points the node's sprite API socket at a fake for the test, so join
// runs as it would on a fly-sprite.
func onSprite(t *testing.T) *devtest.SpriteAPI {
	t.Helper()
	api := devtest.NewSpriteAPI(t)
	prev := remotenode.SpriteAPISocketPath
	remotenode.SpriteAPISocketPath = api.Socket
	t.Cleanup(func() { remotenode.SpriteAPISocketPath = prev })
	return api
}

// methods is each call's method, in order.
func spriteMethods(calls []devtest.SpriteCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Method
	}
	return out
}

// wantHeldThenReleased checks the calls after the job are the hold's PUT (and
// any renewals) on the node's task, ending in one DELETE of it.
func wantHeldThenReleased(t *testing.T, calls []devtest.SpriteCall, path string) {
	t.Helper()
	if len(calls) < 2 || calls[0].Method != http.MethodPut || calls[len(calls)-1].Method != http.MethodDelete {
		t.Fatalf("sprite API calls = %v, want a PUT first and a DELETE last", spriteMethods(calls))
	}
	for _, c := range calls {
		if c.Path != path {
			t.Errorf("sprite API call %s %s, want %s", c.Method, c.Path, path)
		}
	}
	for _, c := range calls[:len(calls)-1] {
		if c.Method != http.MethodPut || !strings.Contains(c.Body, `"expire":"5m"`) {
			t.Errorf("sprite API call %s %q before the release, want a PUT with a 5m expiry", c.Method, c.Body)
		}
	}
}

// endedAtRelease records, when the sprite sees its hold's DELETE, whether
// satelle.dev's fake had by then received the node's end call. It reads false
// until the DELETE arrives.
func endedAtRelease(api *devtest.SpriteAPI, fake *devtest.Node) *atomic.Bool {
	var ended atomic.Bool
	api.OnCall(func(c devtest.SpriteCall) {
		if c.Method == http.MethodDelete {
			ended.Store(fake.Count(http.MethodPost, "/end") == 1)
		}
	})
	return &ended
}

// AC1: a story node on a sprite holds it awake for its whole serve and releases
// the hold when the serve ends.
func TestNodeStoryServeHoldsTheSpriteForItsJob(t *testing.T) {
	api := onSprite(t)
	r := newNodeRig(t)
	run, fake, joined, spec := r.open(t)
	ended := endedAtRelease(api, fake)
	if calls := api.Calls(); len(calls) == 0 || calls[0].Method != http.MethodPut {
		t.Errorf("sprite API calls while the node serves = %v, want the hold's PUT", spriteMethods(calls))
	}
	for _, c := range api.Calls() {
		if c.Method == http.MethodDelete {
			t.Errorf("the hold was released while the node still serves")
		}
	}
	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	wantHeldThenReleased(t, api.Calls(), "/v1/tasks/satelle-"+strings.ReplaceAll(strings.ToLower(spec.Session), "_", "-"))
	// sty_8637f1f1 AC3: the node tells satelle.dev it has ended before the
	// sprite, released, may pause it.
	if !ended.Load() {
		t.Errorf("the sprite hold was released before satelle.dev received the node's end call")
	}
}
