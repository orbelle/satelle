package upgrade

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestStatusMapRefusesAPartWorkflow(t *testing.T) {
	ws := map[string]*workflow.Workflow{
		"default": {Name: "default", States: map[string]workflow.State{"backlog": {Start: true}}},
		"x/infra": {Name: "x/infra", Part: "x", States: map[string]workflow.State{"intake": {Start: true}}},
	}
	m := &StatusMap{Status: map[string]Target{"open": {Workflow: "x/infra", State: "intake"}, "new": {Workflow: "default", State: "backlog"}}}
	bad := m.check(ws)
	if len(bad) != 1 || !strings.Contains(bad[0], "open -> x/infra/intake") || !strings.Contains(bad[0], "repository x's workflow") {
		t.Fatalf("check = %q, want one refusal of the part target", bad)
	}
}
