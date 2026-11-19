//go:build integration

package tests

import (
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestJudgeDiffersIsWired(t *testing.T) {
	routes, err := workflow.Load(filepath.Join("..", ".satelle", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	skills, err := workflow.LoadSkills(filepath.Join("..", ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if sk, ok := skills["satelle-judge-differs-from-performer-check"]; !ok || !sk.IsCheck() {
		t.Fatal("satelle-judge-differs-from-performer-check is not a check skill of this repo")
	}
	for _, c := range []struct{ workflow, from, to string }{
		{"substrate", "in_progress", "done"},
		{"delivery", "in_progress", "integration"},
		{"chore", "in_progress", "integration"},
	} {
		w, ok := routes[c.workflow]
		if !ok {
			t.Errorf("no workflow %s", c.workflow)
			continue
		}
		tr, err := w.Find(c.from, c.to, "")
		if err != nil {
			t.Errorf("%s: %v", c.workflow, err)
			continue
		}
		if len(tr.Groups) == 0 {
			t.Errorf("%s %s -> %s has no gates", c.workflow, c.from, c.to)
			continue
		}
		wired := false
		for _, name := range tr.Groups[0].Gates {
			g := w.Gates[name]
			if g.Skill == "satelle-judge-differs-from-performer-check" {
				wired = true
				if g.Agent != "" || len(g.Panel) != 0 {
					t.Errorf("%s: gate %s has a judge; the check runs with no agent and no panel", c.workflow, name)
				}
			}
		}
		if !wired {
			t.Errorf("%s %s -> %s: no gate in the first group runs satelle-judge-differs-from-performer-check (%v)", c.workflow, c.from, c.to, tr.Groups[0].Gates)
		}
	}
}
