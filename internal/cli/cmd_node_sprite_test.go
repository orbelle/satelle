//go:build !windows

package cli

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
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

// AC1: a task node woken on a sprite holds it awake from before its run starts
// until the run has concluded, then releases the hold.
func TestNodeTaskWakeHoldsTheSpriteForItsRun(t *testing.T) {
	api := onSprite(t)
	r, _ := taskRig(t, "nightly-sweep", true)
	var (
		mu       sync.Mutex
		atWake   []devtest.SpriteCall
		ranSteps bool
	)
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		mu.Lock()
		atWake, ranSteps = api.Calls(), true
		mu.Unlock()
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "held", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)
	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	mu.Lock()
	defer mu.Unlock()
	if !ranSteps {
		t.Fatalf("the run never reached its step\nnode log:\n%s", r.log)
	}
	if len(atWake) == 0 || atWake[0].Method != http.MethodPut {
		t.Errorf("sprite API calls when the run worked = %v, want the hold's PUT", spriteMethods(atWake))
	}
	for _, c := range atWake {
		if c.Method == http.MethodDelete {
			t.Errorf("the hold was released before the run concluded")
		}
	}
	wantHeldThenReleased(t, api.Calls(), "/v1/tasks/satelle-nod-wake")
}

// AC1: a story node on a sprite holds it awake for its whole serve and releases
// the hold when the serve ends.
func TestNodeStoryServeHoldsTheSpriteForItsJob(t *testing.T) {
	api := onSprite(t)
	r := newNodeRig(t)
	run, _, joined, spec := r.open(t)
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
}
