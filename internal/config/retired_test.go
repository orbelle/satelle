package config

import (
	"strings"
	"testing"
)

func TestRetiredSubstrateKnownCases(t *testing.T) {
	dot, ok := LookupRetired("satelle-dot-standard")
	if !ok || dot.Replacement != "satelle-route-standard" {
		t.Fatalf("dot-standard entry: %+v ok=%v", dot, ok)
	}
	coc, ok := LookupRetired("satelle-configuration-over-code")
	if !ok || coc.Replacement != "" {
		t.Fatalf("configuration-over-code entry: %+v ok=%v", coc, ok)
	}
	if _, ok := LookupRetired("satelle-route-standard"); ok {
		t.Fatal("live name must not appear in RetiredSubstrate")
	}
}

// The four bare structure reviewers were retired when structural conformance
// became deterministic code; a citation of one is stale, so each has a row
// (and TestStructureReviewersNotEmbedded pins that none ships).
func TestRetiredStructureReviewers(t *testing.T) {
	for _, name := range []string{
		"satelle-skill-review", "satelle-workflow-review", "satelle-principle-review", "satelle-story-review",
	} {
		e, ok := LookupRetired(name)
		if !ok {
			t.Errorf("%q must be retired", name)
			continue
		}
		if e.Kind != "skills" || e.Replacement != "" || e.Version == "" || e.Note == "" {
			t.Errorf("%q entry: %+v", name, e)
		}
	}
}

func TestFormatRetirementMessage(t *testing.T) {
	rename := FormatRetirementMessage("satelle-dot-standard", RetiredSubstrate["satelle-dot-standard"])
	if !strings.Contains(rename, "renamed to [[satelle-route-standard]]") || !strings.Contains(rename, "satelle migrate --yes") {
		t.Fatalf("rename message: %s", rename)
	}
	gone := FormatRetirementMessage("satelle-configuration-over-code", RetiredSubstrate["satelle-configuration-over-code"])
	if !strings.Contains(gone, "no replacement") || !strings.Contains(gone, "by hand") {
		t.Fatalf("removal message: %s", gone)
	}
}
