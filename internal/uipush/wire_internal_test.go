package uipush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"

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

func TestWirePartWorkflow(t *testing.T) {
	if got := wireWorkflow(&workflow.Workflow{Name: "x/infra", Part: "x"}).Part; got != "x" {
		t.Fatalf("Part %q, want x", got)
	}
	b, err := json.Marshal(wireWorkflow(&workflow.Workflow{Name: "w"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"part"`) {
		t.Errorf("a shared workflow carries a part: %s", b)
	}
}

func TestPartWorkflowDocBodyIsTheLoadedSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "infra.toml")
	if err := os.WriteFile(file, []byte("on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := Source{RepoRoot: dir, Workflows: map[string]*workflow.Workflow{"x/infra": {Name: "x/infra", Part: "x", File: file, Source: []byte("loaded")}}}
	body := func() string {
		t.Helper()
		docs, err := workflowDocs(src)
		if err != nil || len(docs) != 1 {
			t.Fatalf("workflowDocs = %d, %v", len(docs), err)
		}
		var d mirror.Doc
		if err := json.Unmarshal(docs[0], &d); err != nil {
			t.Fatal(err)
		}
		return d.Body
	}
	if got := body(); got != "loaded" {
		t.Fatalf("body %q, want the loaded source", got)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Skip(err)
	}
	if got := body(); got != "loaded" {
		t.Errorf("body %q after the file became a link, want the loaded source", got)
	}
}
