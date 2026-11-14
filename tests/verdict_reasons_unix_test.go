//go:build integration && unix

package tests

import (
	"context"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The reasons rules (sto_b9f9d9c7) are this repo's check skills, run here as
// a gate's after runs them: on the payload a judged gate hands its after list.
const (
	notesRule     = "satelle-verdict-notes-check"
	referenceRule = "satelle-verdict-reference-check"
)

// runRule runs one of this repo's rules on a single judge's answer, or on the
// given members, and returns its exit and output.
func runRule(t *testing.T, rule string, members ...gate.CheckMember) (bool, string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq is required by the verdict rules and is not on PATH")
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := skills[rule]
	if !ok || !sk.IsCheck() {
		t.Fatalf("%s is not a check skill of this repo", rule)
	}
	env := gate.CheckEnv{Item: "sto_1", From: "plan", To: "in_progress", Gate: "binary-scope", Result: "accept", Binding: "reviewer-alt", Members: members}
	if len(members) == 1 {
		env.Result, env.Notes = members[0].Result, members[0].Notes
	}
	passed, out, err := gate.RunCheck(context.Background(), sk.Script, t.TempDir(), env, 0)
	if err != nil {
		t.Fatalf("%s: %v\n%s", rule, err, out)
	}
	return passed, out
}

func judgeSaid(result, notes string) gate.CheckMember {
	return gate.CheckMember{Binding: "reviewer-alt", Result: result, Notes: notes}
}

// AC1: the notes rule refuses a judge whose notes are too short to say what
// it judged, for any verdict, saying the judge gave no reasons; reasoned
// notes, and a carry or check gate (no members), pass.
func TestVerdictNotesRule(t *testing.T) {
	for _, c := range []struct {
		result, notes string
		pass          bool
	}{
		{"accept", "", false},
		{"accept", "ok", false},
		{"reject", "bad", false},
		{"accept", "   spaces   \n do not   count as reasons, not at all   ", false},
		{"accept", "AC1 holds: the plan names internal/gate/gate.go and its test", true},
		{"reject", "AC2 is unmet: nothing records the raw answer of a refused judge", true},
	} {
		passed, out := runRule(t, notesRule, judgeSaid(c.result, c.notes))
		if passed != c.pass {
			t.Errorf("%s %q: passed %v, want %v\n%s", c.result, c.notes, passed, c.pass, out)
		}
		if !c.pass && !strings.Contains(out, "the judge gave no reasons: reviewer-alt answered "+c.result) {
			t.Errorf("%s %q: output %q", c.result, c.notes, out)
		}
	}
	if passed, out := runRule(t, notesRule); !passed {
		t.Errorf("no members (a carry or a check gate) refused: %s", out)
	}
	// A panel is judged member by member: one silent member refuses it.
	passed, out := runRule(t, notesRule,
		gate.CheckMember{Binding: "reviewer-grok", Result: "accept", Notes: "AC1 holds: the plan names internal/gate/gate.go and its test"},
		gate.CheckMember{Binding: "reviewer-alt", Result: "accept", Notes: ""})
	if passed || !strings.Contains(out, "reviewer-alt answered accept with 0 characters") || strings.Contains(out, "reviewer-grok") {
		t.Errorf("panel: %v %q", passed, out)
	}
}

// exe9a209ff1Notes are the notes reviewer-gemini-3.8-flash-high gave with its
// accept on validate-after of v1.0.65's drift review run exe_9a209ff1, first
// presentation (2026-11-14T03:59:42Z), copied from that run's gate.run: they
// describe the drift-report but name no record path (sto_09b81217).
const exe9a209ff1Notes = "drift-report confirms satelle 1.0.65 checked with drift-check exit 0; base..head is v1.0.64..v1.0.65 with no new DRIFT lines or claim warnings; open drift fix item sto_49749bc9 lists carried findings; 'no drift' reported and no changes made inline."

// AC2: the reference rule refuses an accept that neither quotes the work nor
// names a file, criterion or record path; a reject passes it.
func TestVerdictReferenceRule(t *testing.T) {
	for _, c := range []struct {
		result, notes string
		pass          bool
	}{
		{"accept", "looks good, everything is fine and complete here", false},
		{"accept", "the change is correct and the tests are thorough enough", false},
		{"accept", "checked internal/gate/gate.go:470, the error branch", true},
		{"accept", "AC2 holds as written", true},
		{"accept", "read attachments/plan before judging", true},
		{"accept", `the plan says "a dispatch failure is an error" and does so`, true},
		{"accept", "the edit to gate.go keeps the panel order", true},
		{"accept", "criterion 3 is met", true},
		{"reject", "looks wrong to me, not acceptable at all", true},
		// sto_09b81217 AC2: the notes exe_9a209ff1 was refused for, and the
		// same notes once they name a record path of the run.
		{"accept", exe9a209ff1Notes, false},
		{"accept", exe9a209ff1Notes + " (attachments/task)", true},
		{"accept", exe9a209ff1Notes + " (attachments/drift-report)", true},
		{"accept", exe9a209ff1Notes + " (attachments/release-drift)", true},
		{"accept", exe9a209ff1Notes + " (attachments/release-kept)", true},
		{"accept", exe9a209ff1Notes + " (attachments/release-evidence)", true},
		{"accept", exe9a209ff1Notes + " (.satelle/runs/exe_9a209ff1/attachments/drift-report)", true},
	} {
		passed, out := runRule(t, referenceRule, judgeSaid(c.result, c.notes))
		if passed != c.pass {
			t.Errorf("%s %q: passed %v, want %v\n%s", c.result, c.notes, passed, c.pass, out)
		}
		if !c.pass && !strings.Contains(out, "the judge gave no reasons: reviewer-alt accepted without quoting the work") {
			t.Errorf("%s %q: output %q", c.result, c.notes, out)
		}
	}
	if passed, out := runRule(t, referenceRule); !passed {
		t.Errorf("no members refused: %s", out)
	}
}

// The rules are declared, not defaulted: every judged gate of this repo lists
// the notes rule first, and exactly the gates that judge a plan or a change
// list the reference rule second. The init template declares neither.
func TestVerdictRulesDeclaredOnTheRepoGates(t *testing.T) {
	routes, err := workflow.Load("../.satelle/workflows")
	if err != nil {
		t.Fatal(err)
	}
	reasonsFree := map[string]bool{"blocked-review": true, "cancel-review": true}
	var judged, referenced []string
	for wf, w := range routes {
		for name, g := range w.Gates {
			if len(g.Judges()) == 0 {
				if slices.Contains(g.After, notesRule) || slices.Contains(g.After, referenceRule) {
					t.Errorf("%s gates.%s is a check gate yet lists a reasons rule", wf, name)
				}
				continue
			}
			judged = append(judged, wf+"/"+name)
			if len(g.After) == 0 || g.After[0] != notesRule {
				t.Errorf("%s gates.%s: after %v does not start with %s", wf, name, g.After, notesRule)
			}
			hasRef := len(g.After) > 1 && g.After[1] == referenceRule
			if hasRef {
				referenced = append(referenced, wf+"/"+name)
			}
			if hasRef == reasonsFree[name] {
				t.Errorf("%s gates.%s: after %v (reference rule wanted: %v)", wf, name, g.After, !reasonsFree[name])
			}
		}
	}
	sort.Strings(judged)
	sort.Strings(referenced)
	if len(judged) != 23 || len(referenced) != 16 {
		t.Errorf("judged gates %d %v; referenced %d %v", len(judged), judged, len(referenced), referenced)
	}
	tmpl, err := workflow.Load("../internal/kernel/template/workflows")
	if err != nil {
		t.Fatal(err)
	}
	for wf, w := range tmpl {
		for name, g := range w.Gates {
			if slices.Contains(g.After, notesRule) || slices.Contains(g.After, referenceRule) {
				t.Errorf("the init template's %s gates.%s declares a reasons rule", wf, name)
			}
		}
	}
}
