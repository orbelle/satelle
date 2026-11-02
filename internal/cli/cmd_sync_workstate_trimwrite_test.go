package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
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

// AC1: a status change, a tag change and a story log on a story whose history is
// on the server each append onto the hosted head, and push publishes them.
func TestWriteOnTrimmedStoryBuildsOnHostedHead(t *testing.T) {
	ts, f, repo := hostedTeamRepo(t)
	ungatedLifecycle(t, repo)
	ids := trimmedStories(t, f, ts.URL, repo,
		workitem.CreateInput{Title: "status"}, workitem.CreateInput{Title: "tags"}, workitem.CreateInput{Title: "log"})
	hosted := map[string][]string{}
	for _, id := range ids {
		hosted[id] = f.chain.stored("probe", id)
	}

	writes := map[string][]string{
		ids[0]: {"story", "set", ids[0], "--status", "plan"},
		ids[1]: {"story", "set", ids[1], "--add-tags", "found-in:here"},
		ids[2]: {"story", "log", ids[2], "--kind", "note", "--data", "text=a note"},
	}
	written := map[string][]string{}
	for _, id := range ids {
		if out, err := runRoot(t, writes[id]...); err != nil {
			t.Fatalf("%v: %v\n%s", writes[id], err, out)
		}
		if hasMark(t, id, ledger.MarkHistoryRemote) {
			t.Errorf("%s is still marked remote after a write", id)
		}
		written[id] = assertBuiltOnHostedHead(t, id, hosted[id])
	}
	assertPushPublishes(t, ts.URL, repo, f, written)
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

// AC1: a readiness round records on the story it readies, and on an epic's
// backlog children; each of those, trimmed, is fetched first, so the round's
// events build on each hosted head and push publishes them.
func TestReadyOnTrimmedStoryBuildsOnHostedHead(t *testing.T) {
	for _, epic := range []bool{false, true} {
		name := map[bool]string{false: "story", true: "epic child"}[epic]
		t.Run(name, func(t *testing.T) {
			ts, f, repo := hostedTeamRepo(t)
			readinessRoute(t, repo)
			ins := []workitem.CreateInput{{Title: "to ready", Body: "b", AcceptanceCriteria: "1. ok", Category: "fix", Tags: []string{"epic:trim"}}}
			if epic {
				ins = append(ins, workitem.CreateInput{Title: "the epic", Body: "b", AcceptanceCriteria: "1. ok", Category: "epic-parent", Tags: []string{"epic:trim"}})
			}
			ids := trimmedStories(t, f, ts.URL, repo, ins...)
			story, target := ids[0], ids[0]
			if epic {
				target = ids[1]
			}
			hosted := f.chain.stored("probe", story)

			// The round's own outcome is not under test: the planner cannot run
			// here, so it is not accepted, but it is recorded on the story.
			out, _ := runRoot(t, "story", "ready", target)
			if !strings.Contains(out, story) {
				t.Fatalf("story ready %s did not ready %s:\n%s", target, story, out)
			}
			written := assertBuiltOnHostedHead(t, story, hosted)
			assertPushPublishes(t, ts.URL, repo, f, map[string][]string{story: written})
		})
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

// localCancel is the sty_9b3fefea local root: a reviewed backlog → cancelled
// move written while the story's history was on the server, so its first event
// builds on nothing.
func localCancel(id string) []ledger.Entry {
	ev := func(kind, actor, body, payload string) ledger.Entry {
		return ledger.Entry{ID: ledger.NewID(), StoryID: id, Kind: kind, Actor: actor, Body: body, Payload: json.RawMessage(payload)}
	}
	version := ev(ledger.KindWorkflowVersion, "satelle", "workflow version", `{}`)
	version.Refs = json.RawMessage(`{"workflow_version":"h"}`)
	return relinked(
		version,
		ev(ledger.KindReviewAccept, "satelle-story-cancel-review", "accept", `{"to":"cancelled"}`),
		ev(ledger.KindStatusTransition, "bob", "backlog → cancelled", `{"from":"backlog","to":"cancelled"}`),
		ev(ledger.KindStepSummary, "summariser", "cancelled: superseded", `{"to":"cancelled"}`),
	)
}

// forkedCancel reproduces sty_9b3fefea in team scope: a published backlog story is
// trimmed, cancelled here on a local root, and its row says cancelled. fetched
// runs `sync workstate fetch` after, as the field did, which links the hosted
// chain onto the local root. Push refuses the story either way.
func forkedCancel(t *testing.T, fetched bool) (ts *httptest.Server, f *fakeWorkstateServer, repo, id string, hosted, own []string) {
	t.Helper()
	ts, f, repo = hostedTeamRepo(t)
	cancellableLifecycle(t, repo)
	id = trimmedStories(t, f, ts.URL, repo, workitem.CreateInput{Title: "cancelled here"})[0]
	hosted = f.chain.stored("probe", id)
	root := localCancel(id)
	setLocalChain(t, id, root)
	own = idsOf(root)
	// The cancel left the row as its verb does: rebuilt from the local chain.
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		row, err := db.Stories.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row, err = verb.RebuildRow(ctx, db.Stories, db.Ledger, row, time.Now()); err != nil || row.Status != "cancelled" {
			t.Fatalf("rebuild after the local cancel: status %q, %v", row.Status, err)
		}
	})
	if fetched {
		if out, err := runRoot(t, "sync", "workstate", "fetch", id); err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
	}
	// Moved when the store holds only the local root; withheld for drift once the
	// fetch folds the chain back to backlog under the cancelled row.
	if out, err := pushRaw(t, ts.URL); err == nil || !(strings.Contains(out, "moved since its start") || strings.Contains(out, "withheld "+id)) {
		t.Fatalf("push of the forked story = %v\n%s, want it refused", err, out)
	}
	return ts, f, repo, id, hosted, own
}

// AC2, AC3: rechain puts a story forked by a local cancel back on its hosted
// chain with every local event replayed in order and none dropped, without
// --force; story get, story list and story reconcile then agree it is cancelled,
// and push publishes it.
func TestRechainReplaysLocalWork(t *testing.T) {
	for _, fetched := range []bool{true, false} {
		name := map[bool]string{true: "hosted chain fetched", false: "local root alone"}[fetched]
		t.Run(name, func(t *testing.T) {
			ts, f, repo, id, hosted, own := forkedCancel(t, fetched)
			before := chainShape(localChain(t, id))

			out, err := runRoot(t, "sync", "workstate", "rechain", id, "--dry-run", "--server", ts.URL)
			if err != nil || !strings.Contains(out, "would rechain "+id) || !strings.Contains(out, "replay 4 local event(s)") {
				t.Fatalf("rechain --dry-run = %v\n%s, want the four local events replayed", err, out)
			}
			if got := chainShape(localChain(t, id)); !slices.Equal(got, before) {
				t.Fatalf("--dry-run changed the chain: %v, want %v", got, before)
			}
			out, err = runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL)
			if err != nil || !strings.Contains(out, "rechained "+id) || !strings.Contains(out, "replay 4 local event(s)") {
				t.Fatalf("rechain = %v\n%s", err, out)
			}

			order, err := chainOrder(localChain(t, id))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := idsOf(order), append(slices.Clone(hosted), own...); !slices.Equal(got, want) {
				t.Fatalf("chain after rechain = %v, want the hosted chain then the local work %v", got, want)
			}
			if base := order[len(hosted)].BaseID; base != hosted[len(hosted)-1] {
				t.Errorf("the first replayed event builds on %q, want the hosted latest %q", base, hosted[len(hosted)-1])
			}

			if got := storyStatus(t, id); got != "cancelled" {
				t.Errorf("story get status = %q, want cancelled", got)
			}
			list, err := runRoot(t, "story", "list", "--status", "cancelled")
			var rows []workitem.Item
			if err != nil || json.Unmarshal([]byte(list), &rows) != nil || !slices.ContainsFunc(rows, func(it workitem.Item) bool { return it.ID == id }) {
				t.Errorf("story list --status cancelled = %v\n%s, want %s", err, list, id)
			}
			if out, err := runRoot(t, "story", "reconcile"); err != nil || strings.Contains(out, id) {
				t.Errorf("story reconcile = %v\n%s, want the row and the chain to agree", err, out)
			}
			assertPushPublishes(t, ts.URL, repo, f, map[string][]string{id: own})
		})
	}
}

// AC2's guard: a hosted chain that moved the story's status since the local root
// was written is a concurrent move, and rechain refuses it with both statuses,
// changing nothing.
func TestRechainRefusesReplayOnMovedStatus(t *testing.T) {
	ts, f, _, id, _, _ := forkedCancel(t, false)
	otherMachineTransition(t, f, id, "backlog", "plan")
	before := chainShape(localChain(t, id))
	for _, args := range [][]string{{"--dry-run"}, {}} {
		out, err := runRoot(t, append([]string{"sync", "workstate", "rechain", id, "--server", ts.URL}, args...)...)
		if err == nil || !strings.Contains(out, "moves from backlog but the hosted chain folds to plan") {
			t.Fatalf("rechain %v = %v\n%s, want it refused naming both statuses", args, err, out)
		}
		if got := chainShape(localChain(t, id)); !slices.Equal(got, before) {
			t.Errorf("rechain %v changed the chain: %v, want %v", args, got, before)
		}
	}
}
