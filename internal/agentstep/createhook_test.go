package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
)

// hookDocs wraps a workflow body as the doc list WorkflowConsistency consumes.
// A PACKED route fixture is split back into its two halves first: packed, it is
// two `[meta]` tables in one string, which is not a document any reader can make
// sense of.
func hookDocs(body string) []docindex.Doc {
	if done, step, ok := strings.Cut(body, routeHalfSplit); ok {
		return []docindex.Doc{
			{Kind: "workflows", Name: "done", Body: done},
			{Kind: "workflows", Name: "step", Body: step},
		}
	}
	return []docindex.Doc{{Kind: "workflows", Name: "w", Body: body}}
}

// doneHalf returns the declaration-of-done half of a packed route fixture — the
// half a lifecycle hook is declared on.
func doneHalf(packed string) string {
	done, _, _ := strings.Cut(packed, routeHalfSplit)
	return done
}

// hookWF declares create review through the explicit `hooks:` form, allocating
// it to a NAMED reviewer binding — the allocation the old empty-selector path
// could not express.
var hookWF = func() string {
	// A lifecycle hook is workflow FRONTMATTER, and a derived route declares it on
	// its declaration of done (sty_9835070d).
	base := spineWF("", "", "", "in_progress|executor", "done")
	// `[[meta.hooks]]` is an array-of-tables under [meta], so it goes AFTER the
	// meta scalars and BEFORE the first category table — a table header ends the
	// section it follows.
	return strings.Replace(base, "scope = \"system\"\n\n",
		"scope = \"system\"\n\n[[meta.hooks]]\noperation = \"create_review\"\n"+
			"skill = \"my-create-review\"\nagent = \"strict-reviewer\"\n\n", 1)
}()

// namedReviewer is an isolated read-only reviewer binding a hook can allocate.
func namedReviewer(model string) config.AgentBinding {
	return config.AgentBinding{
		Role:    config.RoleReviewer,
		Command: agentcli.DefaultClaudeCommand,
		Tools:   "Read,Grep,Glob",
		Model:   model,
	}
}

// TestReviewCreateUsesTheDeclaredAgent is AC1's proof: a workflow declaring an
// `agent:` on its create-review hook runs that binding, not the invisible
// `[reviewer]` fallback the empty selector used to resolve to. The named binding
// runs its OWN harness, so the bootstrap runner must not be the one invoked.
func TestReviewCreateUsesTheDeclaredAgent(t *testing.T) {
	g, bootstrap := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: hookWF, skillBody: "content rubric", skillFound: true})

	var asked []string
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		asked = append(asked, name)
		if name == "strict-reviewer" {
			return namedReviewer("opus"), true
		}
		return config.AgentBinding{}, false
	})

	// The named binding builds its own runner from the command template, so the
	// call fails at exec (no such binary in the test env) rather than silently
	// falling back. Either outcome proves the point we are asserting: the engine
	// asked for the DECLARED section.
	_, _ = g.ReviewCreate(context.Background(), validDraft)

	if len(asked) == 0 {
		t.Fatal("the declared agent was never resolved — create review still uses the empty selector")
	}
	for _, name := range asked {
		if name != "strict-reviewer" {
			t.Errorf("resolved %q, want the declared strict-reviewer", name)
		}
	}
	if bootstrap.got.SystemPrompt != "" {
		t.Error("a named hook binding must run its own harness, not the bootstrap runner")
	}
}

// TestReviewCreateUndeclaredHookStaysDeterministic pins the degradation
// contract through the new path: a workflow declaring no hook keeps creation
// structure-only, with no agent invoked.
func TestReviewCreateUndeclaredHookStaysDeterministic(t *testing.T) {
	g, r := newEngine(t, `{"decision":"reject","notes":"should not run"}`,
		fakeDocs{workflow: plainWF, skillFound: false})
	dec, err := g.ReviewCreate(context.Background(), validDraft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept || dec.Skill != structureSkill {
		t.Fatalf("no hook → deterministic accept, got %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Error("no content reviewer should run when the workflow declares none")
	}
}

// TestReviewCreateExecutionSkipsContentHook pins sty_313f9018 AC1: creating an
// execution runs the deterministic structure check and never the create_review
// content hook — whatever the category, and whether the hook allocates the
// default reviewer or a named binding. The stub reviewer would reject.
func TestReviewCreateExecutionSkipsContentHook(t *testing.T) {
	execDraft := validDraft
	execDraft.Kind = "execution"
	execDraft.Category = "chore" // workitem.RunCategory, as the wake creates it

	for name, wf := range map[string]string{"shorthand": createWF, "named binding": hookWF} {
		t.Run(name, func(t *testing.T) {
			g, r := newEngine(t, `{"decision":"reject","notes":"must not run"}`,
				fakeDocs{workflow: wf, skillBody: "content rubric", skillFound: true})
			g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
				t.Errorf("an execution's create must not resolve the hook's agent %q", name)
				return config.AgentBinding{}, false
			})
			dec, err := g.ReviewCreate(context.Background(), execDraft)
			if err != nil {
				t.Fatal(err)
			}
			if !dec.Gated || !dec.Accept || dec.Skill != structureSkill {
				t.Fatalf("execution create: want gated accept by %s, got %+v", structureSkill, dec)
			}
			if r.got.SystemPrompt != "" {
				t.Error("the content reviewer ran on an execution's create")
			}
		})
	}

	// The structure check still runs first: a malformed execution is rejected.
	g, r := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: createWF, skillBody: "content rubric", skillFound: true})
	bad := execDraft
	bad.Body = ""
	dec, err := g.ReviewCreate(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept || dec.Skill != structureSkill {
		t.Fatalf("malformed execution: want %s reject, got %+v", structureSkill, dec)
	}
	if r.got.SystemPrompt != "" {
		t.Error("no reviewer may run on a malformed execution")
	}
}

// TestReviewCreateStoryAndTaskStillRunHook pins sty_313f9018 AC2: the execution
// carve-out leaves story and task creation judged by the declared create_review
// hook, as before.
func TestReviewCreateStoryAndTaskStillRunHook(t *testing.T) {
	for _, kind := range []string{"story", "task"} {
		t.Run(kind, func(t *testing.T) {
			g, r := newEngine(t, `{"decision":"reject","notes":"not aligned"}`,
				fakeDocs{workflow: createWF, skillBody: "content rubric", skillFound: true})
			draft := validDraft
			draft.Kind = kind
			dec, err := g.ReviewCreate(context.Background(), draft)
			if err != nil {
				t.Fatal(err)
			}
			if !dec.Gated || dec.Accept || dec.Skill != "my-create-review" {
				t.Fatalf("%s create: want gated reject by the hook, got %+v", kind, dec)
			}
			if r.got.SystemPrompt == "" {
				t.Errorf("%s create: the content reviewer must have run", kind)
			}
		})
	}
}

// TestWorkflowConsistencyReportsHookDefects pins AC5's declaration half: an
// unresolved hook skill and a malformed/unknown declaration are both surfaced by
// the consistency check `satelle workflow validate` and `agent validate` run.
func TestWorkflowConsistencyReportsHookDefects(t *testing.T) {
	cases := map[string]struct {
		body    string
		resolve func(string) bool
		want    string
	}{
		"unresolved hook skill": {
			body:    hookWF,
			resolve: func(string) bool { return false },
			want:    "does not resolve in the substrate",
		},
		"unresolved shorthand skill": {
			body:    createWF,
			resolve: func(string) bool { return false },
			want:    "create_review",
		},
		"unknown operation": {
			body:    "---\nname: w\napplies_to: [\"*\"]\nhooks:\n  - operation: close_review\n    skill: s\n---\n",
			resolve: func(string) bool { return true },
			want:    "unknown operation",
		},
		"hook with no skill": {
			body:    "---\nname: w\napplies_to: [\"*\"]\nhooks:\n  - operation: create_review\n---\n",
			resolve: func(string) bool { return true },
			want:    "declares no skill",
		},
		"declared both ways": {
			body:    "---\nname: w\napplies_to: [\"*\"]\ncreate_review: a\nhooks:\n  - operation: create_review\n    skill: b\n---\n",
			resolve: func(string) bool { return true },
			want:    "both",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			problems := WorkflowConsistency(hookDocs(c.body), c.resolve)
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, c.want) {
				t.Errorf("want a problem mentioning %q, got %v", c.want, problems)
			}
			if !strings.Contains(joined, "workflow ") {
				t.Errorf("the problem must name the workflow: %v", problems)
			}
		})
	}
}

// TestWorkflowConsistencyAcceptsAHealthyHook guards against the checks above
// firing on a well-formed declaration.
func TestWorkflowConsistencyAcceptsAHealthyHook(t *testing.T) {
	problems := WorkflowConsistency(hookDocs(hookWF), func(string) bool { return true })
	for _, p := range problems {
		if strings.Contains(p, "create_review") || strings.Contains(p, "hooks:") {
			t.Errorf("a healthy hook must produce no problem: %v", problems)
		}
	}
}
