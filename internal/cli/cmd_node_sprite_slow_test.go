//go:build clislow && !windows

package cli

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// AC1: a task node woken on a sprite holds it awake from before its run starts
// until the run has concluded, then releases the hold.
func TestSlowNodeTaskWakeHoldsTheSpriteForItsRun(t *testing.T) {
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

// sty_8637f1f1 AC1, AC2: a task node whose run concludes, whatever the outcome,
// reports it concluded, then tells satelle.dev it has ended, and only then
// releases its sprite hold, so a sprite that pauses on the release never leaves
// the node silent and listed lost. The end is its last report: no status
// follows the concluded one.
func TestSlowNodeTaskWakeEndsBeforeReleasingTheSprite(t *testing.T) {
	for _, tc := range []struct {
		name, record string // record is the outcome the performer records; "" records none
		want         string
	}{
		{"accept", verb.OutcomeAccept, verb.OutcomeAccept},
		{"reject", verb.OutcomeReject, verb.OutcomeReject},
		{"performed", "", verb.OutcomePerformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := onSprite(t)
			r, subject := taskRig(t, "nightly-sweep", true)
			useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
				if tc.record == "" {
					return nil
				}
				return dispatchInto(context.Background(), "execution-record",
					map[string]any{"id": item.ID, "output": "ran", "outcome": tc.record}, &struct{}{})
			}}, 0)
			w := r.wake(t)
			ended := endedAtRelease(api, w.fake)
			if err := waitJoin(t, w.joined); err != nil {
				t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
			}
			wantHeldThenReleased(t, api.Calls(), "/v1/tasks/satelle-nod-wake")
			if !ended.Load() {
				t.Errorf("the sprite hold was released before satelle.dev received the node's end call")
			}

			steps := runSteps(t, w.fake, subject.ID())
			if len(steps) == 0 || steps[len(steps)-1] != "concluded//"+tc.want {
				t.Errorf("heartbeats = %v, want them to end concluded//%s", steps, tc.want)
			}
			if st := w.fake.Statuses(); len(st) == 0 || st[len(st)-1]["run_state"] != "concluded" {
				t.Errorf("the last status = %v, want the concluded one", st)
			}
			var order []string
			for _, c := range w.fake.Calls() {
				switch {
				case c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status"):
					order = append(order, "status")
				case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end"):
					order = append(order, "end")
				}
			}
			calls := w.fake.Calls()
			if got := strings.Join(order, " "); !strings.HasSuffix(got, "status end") || strings.Count(got, "end") != 1 {
				t.Errorf("status and end calls = %s, want the concluded status then one end", got)
			}
			if last := calls[len(calls)-1]; last.Method != http.MethodPost || !strings.HasSuffix(last.Path, "/end") {
				t.Errorf("the last call to satelle.dev = %s %s, want the end call", last.Method, last.Path)
			}
		})
	}
}
