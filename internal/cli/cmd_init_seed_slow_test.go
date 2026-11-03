//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// AC1: init rewriting satelle.toml during the engagement is not the story's
// change, nor is init rewriting its own bytes again with nothing between.
func TestSlowInitRewriteLeavesStoryDiff(t *testing.T) {
	repo, e := seededStory(t)
	editAfterEngage(t, repo, mine, mineSkill)

	initUnwired(t, repo, e)
	if rows := seedRows(t, e.db, seedTomlRel); len(rows) == 0 {
		t.Fatal("init healed satelle.toml but recorded no seed row for it")
	}
	res := e.substrateDiff()
	if !slices.Contains(res.Files, mine) {
		t.Fatalf("the story's own substrate edit must be listed, got %v", res.Files)
	}
	if diffLists(res, seedTomlRel) {
		t.Fatalf("init's rewrite must not be in the story's diff:\nfiles %v\nprocess %v\nstat:\n%s", res.Files, res.ProcessRootFiles, res.Stat)
	}

	rewriteAgain(t)
	initUnwired(t, repo, e)
	wantChained(t, e.db, seedTomlRel)
	if res := e.substrateDiff(); diffLists(res, seedTomlRel) {
		t.Fatalf("a second init rewrite of init's own bytes must stay out: %v", res.Files)
	}
}

// AC3: the transition-time change record leaves init's rewrite out too.
func TestSlowInitRewriteLeavesChangeRecord(t *testing.T) {
	repo, e := seededStory(t)
	editAfterEngage(t, repo, mine, mineSkill)
	initUnwired(t, repo, e)
	e.dispatch("story-set", map[string]any{"id": e.id, "status": "done"})
	files := e.lastChangeRecord()
	if slices.Contains(files, seedTomlRel) {
		t.Errorf("change_record must omit init's rewrite, got %v", files)
	}
	if !slices.Contains(files, mine) {
		t.Errorf("change_record must keep the story's own edit, got %v", files)
	}
}

// AC5: a file changed in the window before init rewrote it stays listed, and a
// later init rewriting init's own bytes does not clean it.
func TestSlowStoryEditThenInitStillListed(t *testing.T) {
	repo := seedRepo(t)
	e := engageSyncStoryIn(t, repo, initRuntimeDB(t, repo))
	e.engage()
	editAfterEngage(t, repo, seedTomlRel, healTrigger(t, repo))

	initUnwired(t, repo, e)
	if res := e.substrateDiff(); !slices.Contains(res.Files, seedTomlRel) {
		t.Fatalf("an edit before init's rewrite must stay listed, got %v", res.Files)
	}
	rewriteAgain(t)
	initUnwired(t, repo, e)
	wantChained(t, e.db, seedTomlRel)
	if res := e.substrateDiff(); !slices.Contains(res.Files, seedTomlRel) {
		t.Fatalf("a later init-to-init rewrite must not clean a dirty start, got %v", res.Files)
	}
	e.dispatch("story-set", map[string]any{"id": e.id, "status": "done"})
	if files := e.lastChangeRecord(); !slices.Contains(files, seedTomlRel) {
		t.Errorf("change_record must keep the in-window edit, got %v", files)
	}
}

// AC2: once the story edits the file init wrote, it is the story's change again.
func TestSlowInitRewriteThenStoryEditListed(t *testing.T) {
	repo, e := seededStory(t)
	initUnwired(t, repo, e)
	raw, err := os.ReadFile(filepath.Join(repo, seedTomlRel))
	if err != nil {
		t.Fatal(err)
	}
	editAfterEngage(t, repo, seedTomlRel, string(raw)+"# the story's edit\n")
	if res := e.substrateDiff(); !slices.Contains(res.Files, seedTomlRel) {
		t.Fatalf("the story's edit after init must be listed, got %v", res.Files)
	}
}

// AC4: the --all heal records each healed repo's row in that repo's own ledger.
func TestSlowInitAllRecordsPerRepo(t *testing.T) {
	isolateUserHome(t)
	repos := []string{t.TempDir(), t.TempDir()}
	for _, r := range repos {
		if err := runInit(io.Discard, r, false, []string{"claude"}); err != nil {
			t.Fatalf("runInit %s: %v", r, err)
		}
		writeRepoFile(t, r, satelleHookScriptRel, "# stale wrapper\n")
	}
	if err := runInitAll(io.Discard, true, nil); err != nil {
		t.Fatalf("init --all: %v", err)
	}
	for i, r := range repos {
		db, err := store.Open(initRuntimeDB(t, r))
		if err != nil {
			t.Fatal(err)
		}
		rows := seedRows(t, db, satelleHookScriptRel)
		all, _ := db.Ledger.List(context.Background(), ledger.ListFilter{Kind: ledger.KindSubstrateSeeded})
		_ = db.Close()
		if len(rows) == 0 || rows[len(rows)-1].BeforeSHA256 != sha256Hex([]byte("# stale wrapper\n")) {
			t.Errorf("repo %d: its own ledger must record the --all heal of %s, got %+v", i, satelleHookScriptRel, rows)
		}
		for _, e := range all {
			var p verb.SubstrateSeedPayload
			_ = json.Unmarshal(e.Payload, &p)
			if p.Root != r {
				t.Errorf("repo %d: ledger holds a row for another repo %s", i, p.Root)
			}
		}
	}
}
