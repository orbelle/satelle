package agentstep

import (
	"context"
	"testing"

	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_ab93f9a6: a functional check runs in the main tree (the process of record)
// and is told which story it judges, so a story engaged from a linked worktree
// is reached through that story and not by moving the check.

// The check's working directory stays the repo root, and the gated story rides
// the context the check is run with.
func TestRunCheckKeepsRepoRootAndNamesStory(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.repoRoot = "/the/main/tree"
	var gotDir, gotStory string
	g.check = func(ctx context.Context, dir, _, _ string) (string, error) {
		gotDir, gotStory = dir, gateStoryFrom(ctx)
		return "", nil
	}
	if dec := g.runCheck(context.Background(), workitem.Item{ID: "sty_wt"}, "satelle-x-check", "true", "{}", ""); !dec.Accept {
		t.Fatalf("check should accept: %+v", dec)
	}
	if gotDir != g.repoRoot {
		t.Errorf("check dir = %q, want the repo root %q", gotDir, g.repoRoot)
	}
	if gotStory != "sty_wt" {
		t.Errorf("check story = %q, want sty_wt", gotStory)
	}
}
