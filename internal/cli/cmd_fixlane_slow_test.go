//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/fixlane"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// AC2 + AC6 through the surface a driver uses: each refused class names itself
// in the CLI error, the claim is a ledger row carrying the class, and the edit
// that follows a refused claim is refused with the class in its deny reason.
func TestSlowFixLaneRefusalsNameTheClass(t *testing.T) {
	laneRepo(t)
	cases := []struct{ path, class string }{
		{"internal/foo.go", fixlane.ClassProductSurface},
		{"internal/foo_test.go", fixlane.ClassProductSurface}, // *_test.go is not special-cased
		{"tests/x.sh", fixlane.ClassProductSurface},
		{".satelle/skills/satelle-code-ac-review.md", fixlane.ClassGateSkill},
		{".satelle/workflows/agents.toml", fixlane.ClassReviewerRubric},
		{".satelle/satelle.toml", fixlane.ClassRepoConfig}, // the bound must not be editable through the lane
		{".satelle/workflows/step.toml", fixlane.ClassWorkflow},
		{".satelle/principles/satelle-yagni.md", fixlane.ClassPrinciple},
		{".satelle/constitution.md", fixlane.ClassPrinciple},
	}
	for _, tc := range cases {
		out, err := runRoot(t, claimArgs(tc.path, "3")...)
		if err == nil || !strings.Contains(out+err.Error(), "class "+tc.class) {
			t.Errorf("claim %s: got %v / %q, want a refusal naming class %s", tc.path, err, out, tc.class)
		}
	}
	out, err := runRoot(t, "fix", "claim", "docs/notes.md", "--reason", "r", "--lines", "3")
	if err == nil || !strings.Contains(out+err.Error(), "class "+fixlane.ClassNoProvingTest) {
		t.Errorf("no proving test: got %v / %q, want class %s", err, out, fixlane.ClassNoProvingTest)
	}

	rows := ledgerRows(t, ledger.KindFixClaim)
	if len(rows) != len(cases)+1 {
		t.Fatalf("every refused claim is a ledger row: got %d, want %d", len(rows), len(cases)+1)
	}
	for i, tc := range cases {
		var p fixlane.ClaimPayload
		if err := json.Unmarshal(rows[i].Payload, &p); err != nil || p.Decision != fixlane.DecisionRefused || p.RefusedClass != tc.class {
			t.Errorf("row %d = %+v (%v), want refused/%s", i, p, err, tc.class)
		}
	}

	// The edit after a refused claim is denied, and says why the lane is closed.
	out, err = runRootIn(t, laneEditEvent("internal/foo.go", "a\n", "b\n"), "hook", "gate")
	if err == nil || !strings.Contains(out+err.Error(), "REFUSED (class "+fixlane.ClassProductSurface) {
		t.Fatalf("edit after a refused claim: %v\n%s — want a denial naming the refused class", err, out)
	}
}
