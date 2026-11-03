package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/runstatus"
)

// fakeRunStatus is the status source a story's run with a team remote is read
// from, standing in for satelle.dev.
type fakeRunStatus struct {
	run    runstatus.Run
	found  bool
	err    error
	remote string
}

func (f *fakeRunStatus) StoryRun(_ context.Context, remote, _ string) (runstatus.Run, bool, error) {
	f.remote = remote
	return f.run, f.found, f.err
}

func useRunStatus(t *testing.T, f *fakeRunStatus, resolveErr error) {
	t.Helper()
	prev := remoteRunSource
	remoteRunSource = func(context.Context, config.Config) (runstatus.Source, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		return f, nil
	}
	t.Cleanup(func() { remoteRunSource = prev })
}

const remoteRunDone = `["*"]
obligations = ["raised", "coded", "opened", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
recover = { step = "in_progress", from = ["review"] }
`

const remoteRunStep = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[opened]
status = "review"
remote_wait = { remote = "pr-team", on = { accept = "done", reject = "in_progress", stopped = "blocked" } }
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["opened"]
`

// storyAtWait is a story moved to the waiting step of a repo whose route
// declares one, with the status source stubbed.
func storyAtWait(t *testing.T) string {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), remoteRunDone, remoteRunStep)
	if out, err := runRootIn(t, "", "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	out, err := runRootIn(t, "", "story", "create", "--title", "wait on the remote", "--body", "b", "--acceptance", "1. a", "--category", "chore")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("story create output: %v\n%s", err, out)
	}
	for _, to := range []string{"in_progress", "review"} {
		if out, err := runRootIn(t, "", "story", "set", created.ID, "--status", to); err != nil {
			t.Fatalf("story set %s: %v\n%s", to, err, out)
		}
	}
	return created.ID
}
