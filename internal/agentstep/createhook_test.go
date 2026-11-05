package agentstep

import (
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
