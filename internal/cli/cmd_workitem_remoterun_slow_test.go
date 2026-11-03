//go:build clislow

package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/runstatus"
)

// story get and story route say where the story's run with the team remote
// stands: queued with its position, running with its step, concluded with its
// outcome.
func TestSlowStoryGetAndRouteShowTheRemoteRun(t *testing.T) {
	id := storyAtWait(t)
	for _, tc := range []struct {
		name string
		run  runstatus.Run
		want string
	}{
		{"queued", runstatus.Run{State: runstatus.Queued, Position: 3}, "queued with pr-team (position 3)"},
		{"running", runstatus.Run{State: runstatus.Running, Step: "review"}, "running with pr-team at step review"},
		{"concluded", runstatus.Run{State: runstatus.Concluded, Outcome: "accept"}, "concluded with pr-team: accept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeRunStatus{run: tc.run, found: true}
			useRunStatus(t, src, nil)

			out, err := runRootIn(t, "", "story", "get", id, "--json")
			if err != nil {
				t.Fatalf("story get: %v\n%s", err, out)
			}
			var got struct {
				RemoteRun struct {
					Remote string `json:"remote"`
					Status string `json:"status"`
				} `json:"remote_run"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("story get --json is not JSON: %v\n%s", err, out)
			}
			if got.RemoteRun.Remote != "pr-team" || got.RemoteRun.Status != tc.want {
				t.Errorf("story get remote_run = %+v, want pr-team / %q\n%s", got.RemoteRun, tc.want, out)
			}
			if src.remote != "pr-team" {
				t.Errorf("the source was asked for remote %q, want the one the step declares", src.remote)
			}

			out, err = runRootIn(t, "", "story", "route", id)
			if err != nil {
				t.Fatalf("story route: %v\n%s", err, out)
			}
			if !strings.Contains(out, "Remote run** — "+tc.want) {
				t.Errorf("story route must show %q:\n%s", tc.want, out)
			}
		})
	}
}

// A run that cannot be read is shown as unknown with its reason; showing a story
// never fails on the network. A story whose step waits on nothing shows no run.
func TestSlowStoryGetShowsAnUnreadableRemoteRunAsUnknown(t *testing.T) {
	id := storyAtWait(t)
	useRunStatus(t, &fakeRunStatus{err: errors.New("satelle.dev is unreachable")}, nil)
	out, err := runRootIn(t, "", "story", "get", id, "--json")
	if err != nil || !strings.Contains(out, "unknown (satelle.dev is unreachable)") {
		t.Fatalf("an unreadable run is unknown, not an error: %v\n%s", err, out)
	}

	useRunStatus(t, nil, errors.New("no team workspace"))
	if out, err = runRootIn(t, "", "story", "route", id); err != nil || !strings.Contains(out, "unknown (no team workspace)") {
		t.Fatalf("a source that cannot be resolved is unknown, not an error: %v\n%s", err, out)
	}

	if out, err = runRootIn(t, "", "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("story set: %v\n%s", err, out)
	}
	if out, err = runRootIn(t, "", "story", "get", id, "--json"); err != nil || strings.Contains(out, "remote_run") {
		t.Fatalf("a step that waits on nothing shows no run: %v\n%s", err, out)
	}
}
