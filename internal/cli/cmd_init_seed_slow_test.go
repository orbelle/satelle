//go:build clislow

package cli

import (
	"slices"
	"testing"
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
