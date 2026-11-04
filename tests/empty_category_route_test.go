//go:build integration

package tests

import (
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestEmptyCategoryTakesTheSpine: the shipped done.toml declares no table for
// category empty, so a story created as empty in a fresh repo renders the same
// route as a feature story (the "*" spine), and not the route of a category
// that has a table of its own (docs).
func TestEmptyCategoryTakesTheSpine(t *testing.T) {
	cats := embeddedRouteCategories(t)
	if slices.Contains(cats, workitem.EmptyCategory) {
		t.Fatalf("the shipped done.toml declares a table for %q (%v) — it must take the \"*\" spine", workitem.EmptyCategory, cats)
	}
	if !slices.Contains(cats, "*") || !slices.Contains(cats, "docs") {
		t.Fatalf("the shipped done.toml must declare \"*\" and docs for this comparison, got %v", cats)
	}

	repo := t.TempDir()
	mustRun(t, testBin, repo, "init")
	route := func(category string) string {
		out := mustRun(t, testBin, repo, "story", "create",
			"--title", "Prove the route with a "+category+" story",
			"--body", "A story taken through the workflow to see which route it resolves.",
			"--acceptance", "1. the story reaches its terminal state",
			"--category", category)
		id := extractID(out, "sty_")
		if id == "" {
			t.Fatalf("no story id for category %s:\n%s", category, out)
		}
		if got := mustRun(t, testBin, repo, "story", "get", id); !strings.Contains(got, `"category": "`+category+`"`) {
			t.Fatalf("story %s was not stored as category %s:\n%s", id, category, got)
		}
		r := mustRun(t, testBin, repo, "story", "route", id)
		_, steps, ok := strings.Cut(r, "## Route")
		if !ok {
			t.Fatalf("story route for %s has no route section:\n%s", id, r)
		}
		return steps
	}

	empty, spine, docs := route(workitem.EmptyCategory), route("feature"), route("docs")
	if spine == docs {
		t.Fatalf("the feature and docs routes render the same — the comparison would prove nothing:\n%s", spine)
	}
	if empty != spine {
		t.Errorf("an empty story does not take the \"*\" spine:\n--- empty ---\n%s\n--- feature (*) ---\n%s", empty, spine)
	}
}
