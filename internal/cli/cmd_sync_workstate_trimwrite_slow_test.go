//go:build clislow

package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// AC1: a status change, a tag change and a story log on a story whose history is
// on the server each append onto the hosted head, and push publishes them.
func TestSlowWriteOnTrimmedStoryBuildsOnHostedHead(t *testing.T) {
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

// AC1: a readiness round records on the story it readies, and on an epic's
// backlog children; each of those, trimmed, is fetched first, so the round's
// events build on each hosted head and push publishes them.
func TestSlowReadyOnTrimmedStoryBuildsOnHostedHead(t *testing.T) {
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

// AC2, AC3: rechain puts a story forked by a local cancel back on its hosted
// chain with every local event replayed in order and none dropped, without
// --force; story get, story list and story reconcile then agree it is cancelled,
// and push publishes it.
func TestSlowRechainReplaysLocalWork(t *testing.T) {
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
