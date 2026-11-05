package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_00212637: in team scope a push leaves a story's history on the server
// (ledger.MarkHistoryRemote). A write to such a story must build on the hosted
// head, not start a chain of its own that every later push refuses as moved.

// trimmedStories caches one published backlog story per input, then trims them
// all in team scope, so each holds a row and no local event.
func trimmedStories(t *testing.T, f *fakeWorkstateServer, server, repo string, ins ...workitem.CreateInput) []string {
	t.Helper()
	longAgo := time.Now().Add(-72 * time.Hour).UTC()
	var ids []string
	for _, in := range ins {
		if in.Status == "" {
			in.Status = "backlog"
		}
		ids = append(ids, cacheItem(t, f, server, repo, in, longAgo))
	}
	team := config.TeamScope
	if n := trimUnder(t, server, repo, &team); n != len(ids) {
		t.Fatalf("trimmed %d stor(y/ies), want %d", n, len(ids))
	}
	for _, id := range ids {
		if localEvents(t, id) != 0 || !hasMark(t, id, ledger.MarkHistoryRemote) {
			t.Fatalf("%s was not trimmed", id)
		}
	}
	return ids
}

// assertBuiltOnHostedHead checks that the local chain of id is the hosted chain
// hosted, then at least one new event, the first of which builds on the hosted
// latest; it returns the new events' ids.
func assertBuiltOnHostedHead(t *testing.T, id string, hosted []string) []string {
	t.Helper()
	order, err := chainOrder(localChain(t, id))
	if err != nil {
		t.Fatalf("%s: local chain: %v", id, err)
	}
	ids := idsOf(order)
	if len(ids) <= len(hosted) || !slices.Equal(ids[:len(hosted)], hosted) {
		t.Fatalf("%s: local chain = %v, want the hosted chain %v and then the write", id, ids, hosted)
	}
	if base := order[len(hosted)].BaseID; base != hosted[len(hosted)-1] {
		t.Errorf("%s: the write's first event builds on %q, want the hosted latest %q", id, base, hosted[len(hosted)-1])
	}
	return ids[len(hosted):]
}

// assertPushPublishes pushes and checks each story's new events reached the
// server with no story refused as moved and no conflict recorded.
func assertPushPublishes(t *testing.T, server, repo string, f *fakeWorkstateServer, written map[string][]string) {
	t.Helper()
	out, err := pushRaw(t, server)
	if err != nil || strings.Contains(out, "moved since") {
		t.Fatalf("push = %v\n%s, want every story published", err, out)
	}
	for id, events := range written {
		stored := f.chain.stored("probe", id)
		for _, e := range events {
			if !slices.Contains(stored, e) {
				t.Errorf("%s: the server holds %v, want the written event %s", id, stored, e)
			}
		}
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after push = %v, want none", c)
	}
}

// readinessRoute gives repo a route whose stories have a readiness step and whose
// epic-parents a container readiness step that readies their children first.
func readinessRoute(t *testing.T, repo string) {
	t.Helper()
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "readied", "coded", "closed"]

[epic-parent]
obligations = ["raised", "ready", "epic-coded", "epic-closed"]
`,
		`[raised]
status = "backlog"
start = true

[ready]
status = "ready"
agent = "planner"
waits_on_children = true
requires = ["raised"]

[readied]
status = "plan"
agent = "planner"
skills = ["plan"]
propose = true
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["readied"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[epic-coded]
status = "in_progress"
agent = "executor"
requires = ["ready"]

[epic-closed]
status = "done"
terminal = true
requires = ["epic-coded"]
`)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
}

// cancellableLifecycle is ungatedLifecycle with an ungated cancel exit.
func cancellableLifecycle(t *testing.T, repo string) {
	t.Helper()
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
cancel = { state = "cancelled" }
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
}
