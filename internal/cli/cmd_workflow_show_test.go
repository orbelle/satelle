package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// showFixture stands up an isolated home + a data dir carrying agentsBody, and
// returns the app `workflow show` renders against. No store is needed: the view
// takes its skill resolver as a parameter.
func showFixture(t *testing.T, agentsBody string) *app.App {
	t.Helper()
	testutil.IsolateHome(t)
	dataDir := filepath.Join(t.TempDir(), ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if agentsBody != "" {
		if err := os.WriteFile(filepath.Join(dataDir, config.AgentsConfigName), []byte(agentsBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &app.App{DataDir: dataDir}
}

func showDoc(fm string) docindex.Doc {
	return docindex.Doc{
		Kind: "workflows", Name: "w",
		Body: "---\nname: w\nscope: project\napplies_to: [\"*\"]\n" + fm + "---\n\n```dot\ndigraph w {\n  backlog [shape=Mdiamond]\n  done [shape=Msquare]\n  backlog -> done [agent=reviewer, prompt=\"@skill:g\"]\n}\n```\n",
	}
}

func render(t *testing.T, a *app.App, doc docindex.Doc, resolves func(string) bool) string {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := renderWorkflowShow(buf, a, doc, resolves); err != nil {
		t.Fatalf("renderWorkflowShow: %v", err)
	}
	return buf.String()
}

// The derived route names where each step's performer binding runs.
func TestWorkflowShowRouteRendersThePerformersPlacement(t *testing.T) {
	var b strings.Builder
	placements := map[string]string{"executor": "team:acme/ops"}
	if err := renderWorkflowRouteWith(&b, routeSourceFixture(), "feature", nil, placements); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "placement:  team:acme/ops") {
		t.Errorf("route lacks the performer's placement:\n%s", b.String())
	}
	b.Reset()
	if err := renderWorkflowRoute(&b, routeSourceFixture(), "feature", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "placement:") {
		t.Errorf("a route rendered without placements printed one:\n%s", b.String())
	}
}
