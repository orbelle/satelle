package agentstep

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_216d5205: a gate round stamps the seat activity with the edge's whole gate
// count and, as each gate returns, how many of them passed and failed — the live
// counts the story list's progress chip reads while the round runs.

// activityLog records every Activity the engine emits, safely across the
// parallel gate loops.
type activityLog struct {
	mu   sync.Mutex
	acts []Activity
}

func (l *activityLog) wire(g *Engine) {
	g.SetActivity(func(_ string, a Activity) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.acts = append(l.acts, a)
	})
}

func (l *activityLog) gates() []Activity {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Activity
	for _, a := range l.acts {
		if a.Gates {
			out = append(out, a)
		}
	}
	return out
}

// gateActivityWF is a three-gate edge (rev-a, rev-b, rev-c) under the given
// scheduling, plus a scoped `when` gate rev-d whose trigger the test makes skip.
func gateActivityWF(par, extra string) string {
	return spineWF("", "", `[[gate]]
skill = "rev-d"
agent = "reviewer"
on = ["in_progress"]
when = "trigger"
`, "in_progress|executor||rev-a,rev-b,rev-c|reviewer|"+par+"|"+extra, "done")
}

func TestGateActivityStampsPassedFailed(t *testing.T) {
	// Serially a reject stops the round, so the rejecting gate runs last there;
	// every scheduler then returns two accepts and one reject.
	for _, sc := range []struct{ name, par, extra, reject string }{
		{"serial", "0", "", "rev-c"},
		{"parallel", "2", "", "rev-b"},
		{"bundle", "", "bundle = true", "rev-b"},
	} {
		t.Run(sc.name, func(t *testing.T) {
			skills := append(bundleSkillDocs("rev-a", "rev-b", "rev-c", "rev-d"), whenCheckSkill())
			r := &bundleRunner{verdict: map[string]string{sc.reject: "reject"}}
			g := New(r, fakeDocs{workflow: gateActivityWF(sc.par, sc.extra), extraSkills: skills}, "/repo", "")
			g.backoff = func(int) time.Duration { return 0 }
			// The `when` gate's trigger exits 1: rev-d is never enqueued.
			g.check = func(context.Context, string, string, string) (string, error) { return "no change", exitErr(1) }
			var log activityLog
			log.wire(g)
			if _, err := bundleGate(t, g); err != nil {
				t.Fatal(err)
			}
			if r.sessionsFor("rev-d") != 0 {
				t.Fatalf("rev-d must be skipped by its when trigger")
			}
			stamps := log.gates()
			if len(stamps) < 3 {
				t.Fatalf("gate stamps = %+v, want an opening, a start and a return at least", stamps)
			}
			opening := 0
			for _, a := range stamps {
				if a.Total != 3 {
					t.Errorf("stamp %+v: Total must be the round's 3 gates (the untriggered when gate excluded)", a)
				}
				if a.Index == 0 {
					opening++
				}
			}
			if opening != 1 || stamps[0].Index != 0 || stamps[0].Passed+stamps[0].Failed != 0 {
				t.Errorf("want exactly one opening stamp first, with nothing returned: %+v", stamps)
			}
			partial := false
			for _, a := range stamps[1 : len(stamps)-1] {
				if n := a.Passed + a.Failed; n > 0 && n < 3 {
					partial = true
				}
			}
			if sc.name != "bundle" && !partial {
				t.Errorf("want an intermediate stamp with some gates returned and others not: %+v", stamps)
			}
			if last := stamps[len(stamps)-1]; last.Passed != 2 || last.Failed != 1 || last.Index != 3 {
				t.Errorf("final stamp = %+v, want 2 passed, 1 failed, 3 done", last)
			}
		})
	}
}

// A performer's dispatch and an executor step stamp the seat too, but they are
// not gates: their stamps carry Gates = false, so no gate count reads as running.
func TestPerformerActivityIsNotGates(t *testing.T) {
	ctx := context.Background()
	wf := spineWF("", "", "", "plan|planner|planning-rubric", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric, skillFound: true})
	g.SetLogDir(t.TempDir(), logfile.Config{})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return &fakeRunner{out: "ok"}, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return logBinding, true })
	var log activityLog
	log.wire(g)
	if _, err := g.DispatchExecutor(ctx, workitem.Item{ID: "sty_p", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	ge, _ := executorEngine(t, "fake -p {system}", "ok")
	log.wire(ge)
	if _, err := ge.PerformExecutorStep(ctx, executorItem, "exec", ""); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range log.acts {
		for _, p := range []string{"dispatch:", "perform:"} {
			if strings.HasPrefix(a.Label, p) {
				seen[p] = true
				if a.Gates || a.Passed+a.Failed != 0 {
					t.Errorf("performer stamp %+v must carry no gate counts", a)
				}
			}
		}
	}
	if !seen["dispatch:"] || !seen["perform:"] {
		t.Errorf("want a dispatch: and a perform: stamp, got %+v", log.acts)
	}
}

// A reviewer_carry round dispatches its fresh reviewers, then the carried ones
// as a confirm pass; both passes stamp the one round tally, so the confirm pass
// continues from the fresh pass's counts against the edge's whole gate list.
func TestCarryRoundStampsWholeEdge(t *testing.T) {
	for _, sc := range carrySchedulers {
		t.Run(sc.name, func(t *testing.T) {
			// rev-a and rev-c carry; rev-b and rev-d are dispatched fresh first.
			prior := []PriorVerdict{accepted("rev-a", "PLAN-A"), rejected("rev-b"), accepted("rev-c", "PLAN-C"), rejected("rev-d")}
			wf := spineWF("", "", "", "in_progress|executor||rev-a,rev-b,rev-c,rev-d|reviewer|"+sc.par+"|"+sc.extra+carryKey, "done")
			r := &bundleRunner{}
			g := New(r, fakeDocs{workflow: wf, extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c", "rev-d")}, "/repo", "")
			g.backoff = func(int) time.Duration { return 0 }
			g.SetDocsResolver(func(context.Context, string) []DocState {
				return []DocState{{Name: "plan", Type: "plan", Body: carryPlan}}
			})
			g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
				return append([]PriorVerdict(nil), prior...)
			})
			var log activityLog
			log.wire(g)
			if dec := carryGate(t, g); !dec.Accept {
				t.Fatalf("every reviewer accepts, so the round accepts: %+v", dec)
			}
			if r.sessionsFor("rev-a") != 1 || r.sessionsFor("rev-b") != 1 {
				t.Fatalf("both passes must dispatch: rev-a %d, rev-b %d", r.sessionsFor("rev-a"), r.sessionsFor("rev-b"))
			}
			stamps := log.gates()
			opening := 0
			confirmStart := -1
			for i, a := range stamps {
				if a.Total != 4 {
					t.Errorf("stamp %+v: Total must be the edge's 4 gates, never a pass's 2", a)
				}
				if a.Index == 0 {
					opening++
				}
				if confirmStart < 0 && a.Index == 3 {
					confirmStart = i
				}
			}
			if opening != 1 {
				t.Errorf("the opening stamp must be emitted once per round, got %d: %+v", opening, stamps)
			}
			if confirmStart < 0 {
				t.Fatalf("no stamp starts the confirm pass at gate 3 of 4: %+v", stamps)
			}
			if c := stamps[confirmStart]; c.Passed != 2 || c.Failed != 0 {
				t.Errorf("first confirm-pass stamp = %+v, want it to continue from 2 passed (not 0 of 2)", c)
			}
			if last := stamps[len(stamps)-1]; last.Index != 4 || last.Passed != 4 || last.Failed != 0 {
				t.Errorf("final stamp = %+v, want 4 done, 4 passed", last)
			}
		})
	}
}
