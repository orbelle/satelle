//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// This repo's workflows carry the judged-change rule the binary no longer
// decides: every move into a terminal done state that no model gate judges
// runs the change-judged check first, and that set is not empty.
func TestJudgeLessDoneMovesRunChangeJudgedFirst(t *testing.T) {
	routes, err := workflow.Load(filepath.Join("..", ".satelle", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	skills, err := workflow.LoadSkills(filepath.Join("..", ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if !skills["satelle-change-judged-check"].IsCheck() {
		t.Fatal("satelle-change-judged-check is not a check skill of this repo")
	}
	var matched []string
	for name, w := range routes {
		for _, tr := range w.Transitions {
			if tr.To != "done" || !w.IsTerminal(tr.To) {
				continue
			}
			judged := false
			for _, g := range tr.Groups {
				for _, gate := range g.Gates {
					judged = judged || !skills[w.Gates[gate].Skill].IsCheck()
				}
			}
			if judged {
				continue
			}
			where := name + ": " + strings.Join(tr.From, ",") + " -> " + tr.To
			matched = append(matched, where)
			if len(tr.Groups) == 0 || len(tr.Groups[0].Gates) == 0 || tr.Groups[0].Gates[0] != "change-judged" {
				t.Errorf("%s: no model gate judges it, and change-judged is not its first gate (%+v)", where, tr.Groups)
				continue
			}
			if got := w.Gates["change-judged"].Skill; got != "satelle-change-judged-check" {
				t.Errorf("%s: gate change-judged runs skill %q, want satelle-change-judged-check", where, got)
			}
		}
	}
	for _, want := range []string{"delivery: approved -> done", "chore: approved -> done"} {
		if !slices.Contains(matched, want) {
			t.Errorf("%s is not among the judge-less moves into done %v", want, matched)
		}
	}
}

// The workflows topic leaves the judged-change rule to configuration: it
// names the check and the judged report, and no longer says the binary
// refuses the move.
func TestWorkflowsHelpLeavesTheJudgedChangeRuleToConfiguration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "internal", "help", "topics", "workflows.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(b)), " ")
	for _, want := range []string{"satelle-change-judged-check", "`judged`"} {
		if !strings.Contains(text, want) {
			t.Errorf("workflows topic does not name %s", want)
		}
	}
	for _, gone := range []string{
		"A terminal state is reached only with a change a judge accepted",
		"compares the item's change now with the latest accepted one",
		"the move is refused, naming the gate",
	} {
		if strings.Contains(text, gone) {
			t.Errorf("workflows topic still says the binary refuses: %q", gone)
		}
	}
}
