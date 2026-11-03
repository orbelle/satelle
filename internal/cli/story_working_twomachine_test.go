package cli

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// workingOnRows is the working_on rows `ledger list` shows for id on the current
// machine, oldest first.
func workingOnRows(t *testing.T, id string) []verb.WorkingOnPayload {
	t.Helper()
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", ledger.KindWorkingOn)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	var entries []ledger.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ledger list is not JSON: %v\n%s", err, out)
	}
	var rows []verb.WorkingOnPayload
	for _, e := range entries {
		var p verb.WorkingOnPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("working_on payload: %v\n%s", err, e.Payload)
		}
		rows = append(rows, p)
	}
	return rows
}

func locations(rows []verb.WorkingOnPayload) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Location)
	}
	return out
}

// workingMachine is a machine of the two-machine tests: a git working tree under
// its own session, so a working_on row can name all three of location, session and
// worktree.
func workingMachine(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer, session string) *holdFixture {
	t.Helper()
	fx := newHoldMachine(t, ts, f, holdNoGit)
	gitInitCommit(t, fx.repo)
	fx.session = session
	return fx
}

// storyHead is the last event of id this machine saw the server accept.
func storyHead(t *testing.T, fx *holdFixture, id string) string {
	t.Helper()
	c, err := hosted.LoadWorkstateCursor(fx.ts.URL, "probe", fx.repo)
	if err != nil {
		t.Fatal(err)
	}
	return c.StoryHeads[id]
}
