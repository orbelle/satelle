package cli

// Story-scoped work-state sync (sty_991b6900): push, pull and snapshot take story
// ids or an epic container id and move only those stories and their ledger rows.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

const scopeTheme = "epic:scope-test"

// scopeToml opts executions in too, so a push that leaked one would show.
const scopeToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"

// scopeFixture is a local store holding an epic of two members, a story outside
// it, and an execution and a task under the first member. Every story has two
// ledger rows.
type scopeFixture struct {
	epic, m1, m2, other, exec, task string
}

func (s scopeFixture) members() []string { return []string{s.m1, s.m2} }

func openScopeDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func seedScopeStore(t *testing.T) scopeFixture {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	mk := func(in workitem.CreateInput) string {
		it, err := db.Stories.Create(ctx, in, time.Now())
		if err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
		return it.ID
	}
	var f scopeFixture
	f.epic = mk(workitem.CreateInput{Kind: workitem.KindStory, Title: "Scope epic", Category: "epic-parent", Tags: []string{scopeTheme}})
	f.m1 = mk(workitem.CreateInput{Kind: workitem.KindStory, Title: "Scope member one", Category: "feature", Tags: []string{scopeTheme}})
	f.m2 = mk(workitem.CreateInput{Kind: workitem.KindStory, Title: "Scope member two", Category: "feature", Tags: []string{scopeTheme}})
	f.other = mk(workitem.CreateInput{Kind: workitem.KindStory, Title: "Scope unrelated", Category: "feature"})
	f.exec = mk(workitem.CreateInput{Kind: workitem.KindExecution, Title: "Execution under m1", ParentID: f.m1})
	f.task = mk(workitem.CreateInput{Kind: workitem.KindTask, Title: "Task under m1", ParentID: f.m1})
	for _, id := range []string{f.epic, f.m1, f.m2, f.other} {
		for _, kind := range []string{"note", "plan"} {
			if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: id, Kind: kind, Body: kind + " for " + id}, time.Now()); err != nil {
				t.Fatalf("ledger %s: %v", id, err)
			}
		}
	}
	return f
}

// localCounts is the number of stories and ledger rows in the local store.
func localCounts(t *testing.T) (stories, rows int) {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	items, err := db.Stories.List(context.Background(), workitem.ListFilter{Kind: workitem.KindStory, Limit: 2000})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := db.Ledger.ListInsertedAfter(context.Background(), 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	return len(items), len(entries)
}

// postedRows is what Apply received for the probe project: item ids and the
// story_id of every ledger row.
func postedRows(f *fakeWorkstateServer) (items, ledgerStories []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, batch := range f.posts["probe"] {
		for _, it := range batch["items"].([]any) {
			items = append(items, it.(map[string]any)["id"].(string))
		}
		for _, e := range batch["ledger"].([]any) {
			sid, _ := e.(map[string]any)["story_id"].(string)
			ledgerStories = append(ledgerStories, sid)
		}
	}
	slices.Sort(items)
	slices.Sort(ledgerStories)
	return items, ledgerStories
}

func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// seedHostedScopeStory puts a story on the fake as changed now, with one ledger row.
func seedHostedScopeStory(f *fakeWorkstateServer, id string, tags []string, category string) {
	at := time.Now().Add(time.Hour).UTC()
	seedHostedStory(f, id, "Hosted "+id, "backlog", at)
	f.mu.Lock()
	rec := f.itemsByID["probe"][id].(map[string]any)
	if category != "" {
		rec["category"] = category
	}
	if tags != nil {
		rec["tags"] = tags
	}
	f.mu.Unlock()
	seedHostedLedger(f, "led_"+id, id, "note", map[string]any{"id": id}, at)
}

// remoteEpic is a hosted epic the local store has none of: the container, two
// members carrying its theme, a story whose parent_id is the container but that
// lacks the tag, and an unrelated story.
type remoteEpic struct {
	epic, m1, m2, child, other string
}

func seedRemoteEpic(f *fakeWorkstateServer) remoteEpic {
	r := remoteEpic{epic: "sty_ep000001", m1: "sty_ep000002", m2: "sty_ep000003", child: "sty_ep000004", other: "sty_ep000005"}
	seedHostedScopeStory(f, r.epic, []string{scopeTheme}, "epic-parent")
	seedHostedScopeStory(f, r.m1, []string{scopeTheme}, "feature")
	seedHostedScopeStory(f, r.m2, []string{scopeTheme}, "feature")
	seedHostedScopeStory(f, r.child, nil, "feature")
	f.mu.Lock()
	f.itemsByID["probe"][r.child].(map[string]any)["parent_id"] = r.epic
	f.mu.Unlock()
	seedHostedScopeStory(f, r.other, nil, "feature")
	return r
}

func requestIDs(f *fakeWorkstateServer) [][]string {
	var out [][]string
	for _, r := range f.snapshotRequests() {
		out = append(out, r.GetStoryIds())
	}
	return out
}

// sty_00212637 AC4: push --dry-run names the scope the binding resolves to, team
// or personal, for every area it would push, never a fixed "personal".
func TestWorkstatePushDryRunNamesResolvedScope(t *testing.T) {
	for _, tc := range []struct{ kind, scope string }{{kindTeam, "team"}, {kindPersonal, "personal"}} {
		t.Run(tc.scope, func(t *testing.T) {
			ts, _ := newFakeWorkstateServer(t)
			seedCred(t, ts.URL)
			workstateRepo(t, scopeToml)
			useScope(t, tc.kind)
			out, err := runRoot(t, "sync", "workstate", "push", "--dry-run", "--server", ts.URL)
			if err != nil {
				t.Fatalf("push --dry-run: %v\n%s", err, out)
			}
			var areas []string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, " -> ") {
					areas = append(areas, l)
					if !strings.HasSuffix(l, " -> "+tc.scope) {
						t.Errorf("dry-run line %q, want it to name the %s scope", l, tc.scope)
					}
				}
			}
			if len(areas) == 0 {
				t.Fatalf("push --dry-run names no area:\n%s", out)
			}
		})
	}
}
