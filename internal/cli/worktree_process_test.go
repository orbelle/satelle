package cli

// sty_ddbe2669 AC1: a story driven in a linked worktree placed outside the main
// tree, with no data dir of its own, walks the main tree's route and passes
// the same gates, while the check it runs writes in the worktree.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createFeatureStory(t *testing.T, title string) string {
	t.Helper()
	out, err := runRoot(t, "story", "create", "--title", title, "--body", "b",
		"--acceptance", "1. a", "--category", "feature")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse story create: %v\n%s", err, out)
	}
	return created.ID
}

func storyRoute(t *testing.T, id string) string {
	t.Helper()
	out, err := runRoot(t, "story", "route", id)
	if err != nil {
		t.Fatalf("story route: %v\n%s", err, out)
	}
	return out
}

func assertPlaceFile(t *testing.T, here string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(here, "wt-driven-place"))
	if err != nil {
		t.Fatalf("place file in %s: %v", here, err)
	}
	got, gerr := filepath.EvalSymlinks(strings.TrimSpace(string(body)))
	want, werr := filepath.EvalSymlinks(here)
	if gerr != nil || werr != nil || got != want {
		t.Fatalf("place file records %q (resolved %q, %v), want %q (resolved %q, %v)",
			strings.TrimSpace(string(body)), got, gerr, here, want, werr)
	}
}

const featureLaneDone = `[meta]
name = "done"
type = "workflow"
scope = "project"
description = "feature lane with a plan step"

[feature]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked", gate = "satelle-story-blocked-review" }
cancel = { state = "cancelled", gate = "satelle-story-cancel-review" }
`

const featureLaneStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
description = "plan step whose gate records the tree it ran in"

[planned]
status = "plan"
reviewers = ["wt-place-check"]
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]
`

const placeCheckSkill = `---
name: wt-place-check
type: skill
description: record the tree this check ran in
---

` + "```check\n" + `printf '%s\n' "$PWD" > wt-driven-place
` + "```\n"
