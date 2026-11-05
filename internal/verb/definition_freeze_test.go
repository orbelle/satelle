package verb_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// wireWithWorkflows wires stores and indexes the given workflows (name → body)
// so story-set freeze resolution can List them.
func wireWithWorkflows(t *testing.T, workflows map[string]string) {
	t.Helper()
	_ = wireWithWorkflowsStore(t, workflows)
}

func wireWithWorkflowsStore(t *testing.T, workflows map[string]string) *store.DB {
	t.Helper()
	withWiring(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range workflows {
		if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"workflows": wfDir}, time.Now()); err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetLeaseStore(db.Leases)
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

// writeRouteFiles lands a routeHalves map in a workflows dir. The route source
// is TOML (sty_81bb0dde), and the extension is load-bearing: a `.md` half is
// REFUSED by name as an unconverted repo, so a fixture written as markdown fails
// every case with a conversion error rather than the behaviour under test.
func writeRouteFiles(t *testing.T, wfDir string, halves map[string]string) {
	t.Helper()
	for name, body := range halves {
		if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A lifecycle is a DERIVED ROUTE — done.toml + step.toml (sty_d953c5d8).
// routeHalves names the two docs a fixture must write; a category-specific lane
// is a `[<category>]` table rather than a second workflow file. Frontmatter is
// the `[meta]` table the TOML form uses (sty_81bb0dde).
func routeHalves(done, step string) map[string]string {
	mk := func(name, what, body string) string {
		return "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"" +
			what + "\"\n\n" + body
	}
	return map[string]string{
		"done": mk("done", "fixture declaration of done", done),
		"step": mk("step", "fixture step catalogue", step),
	}
}

var freezeWF = routeHalves(
	`["*"]
obligations = ["raised", "coded", "closed"]

[triage-cat]
obligations = ["triaged", "t-coded", "t-closed"]
`,
	`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[triaged]
status = "triage"
start = true

[t-coded]
status = "in_progress"
agent = "executor"
requires = ["triaged"]

[t-closed]
status = "done"
terminal = true
requires = ["t-coded"]
`)

func TestDefinitionFreezeEngagedRefusesTitle(t *testing.T) {
	wireWithWorkflows(t, freezeWF)
	// Create stamps workflow: by category with applies_to * → freeze-wf if we
	// also wire a resolver, or stamp via tags after create.
	// Create without resolver leaves no stamp; GoverningWorkflow falls to
	// OrderedWorkflows on category — freeze-wf is the only * match.
	var created workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Freeze me", "category": "feature"}), &created)

	// Engage: leave entry state.
	var engaged workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": created.ID, "status": "in_progress"}), &engaged)
	if engaged.Status != "in_progress" {
		t.Fatalf("status = %q", engaged.Status)
	}

	err := dispatchErr(t, "story-set", map[string]any{"id": created.ID, "title": "changed"})
	msg := err.Error()
	if !strings.Contains(msg, "title") || !strings.Contains(msg, "engaged") {
		t.Fatalf("expected freeze error naming title+engaged, got: %v", err)
	}

	// Unaffected fields still work.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": created.ID, "status": "done"}), &engaged)
	if engaged.Status != "done" {
		t.Errorf("status set failed: %q", engaged.Status)
	}
	json.Unmarshal(call(t, "story-set", map[string]any{
		"id": created.ID, "tags": []string{"ok"},
	}), &engaged)
	if len(engaged.Tags) == 0 {
		t.Error("tags set failed")
	}
	json.Unmarshal(call(t, "story-set", map[string]any{"id": created.ID, "priority": "high"}), &engaged)
	if engaged.Priority != "high" {
		t.Errorf("priority = %q", engaged.Priority)
	}
}

func TestDefinitionFreezeIdenticalResubmitOK(t *testing.T) {
	wireWithWorkflows(t, freezeWF)
	var created workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Same", "category": "feature"}), &created)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": created.ID, "status": "in_progress"}), &created)
	// Resubmitting the same title is not a change — must not freeze.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": created.ID, "title": "Same"}), &created)
	if created.Title != "Same" {
		t.Fatalf("identical resubmit failed: %q", created.Title)
	}
}
