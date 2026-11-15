package uipush

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestWireWorkflowPanel(t *testing.T) {
	name := "Drift"
	if got := wireWorkflow(&workflow.Workflow{Name: "w", Panel: &name}).Panel; got != "Drift" {
		t.Fatalf("Panel %q, want Drift", got)
	}
	if got := wireWorkflow(&workflow.Workflow{Name: "w"}).Panel; got != "" {
		t.Fatalf("Panel %q, want none", got)
	}
}
