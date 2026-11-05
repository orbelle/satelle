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

// TestWorkflowShowRendersShorthandProvenance pins AC2's display half: the scalar
// form is shown as the DEFAULT allocation it is, and attributed to the shorthand
// — an operator can tell a choice from a default.
func TestWorkflowShowRendersShorthandProvenance(t *testing.T) {
	a := showFixture(t, "[reviewer]\nrole = \"reviewer\"\ncommand = \"claude -p --disallowedTools Write,Edit --append-system-prompt {system}\"\ntools = \"Read,Grep,Glob\"\n")
	out := render(t, a, showDoc("create_review: satelle-story-create-review\n"), func(string) bool { return true })

	for _, want := range []string{
		"agent:      agent=reviewer (default, from create_review shorthand)",
		"binding:    local binding [reviewer]",
		"skill:      satelle-story-create-review",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A purely local binding must not claim a catalog source.
	if strings.Contains(out, config.GlobalAgentsPath()) {
		t.Errorf("a local binding must not list the machine-wide catalog as a source:\n%s", out)
	}
}

// TestWorkflowShowUnresolvedHookIsMarkedNotErrored pins the read-only posture:
// a hook naming a section with no binding, or a skill absent from the substrate,
// renders an explicit marker and still succeeds — diagnosing a misconfiguration
// is exactly what this command is for; refusing one is agent validate's job.
func TestWorkflowShowUnresolvedHookIsMarkedNotErrored(t *testing.T) {
	a := showFixture(t, "[reviewer]\nrole = \"reviewer\"\n")
	doc := showDoc("hooks:\n  - operation: create_review\n    skill: absent-skill\n    agent: nobody\n")
	out := render(t, a, doc, func(string) bool { return false })

	for _, want := range []string{
		"binding:    UNRESOLVED — agents.toml declares no [nobody]",
		"skill:      absent-skill  UNRESOLVED in the substrate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestWorkflowShowSurfacesDeclarationProblems pins that a malformed or
// unrecognised declaration is visible here too, rather than only in validate.
func TestWorkflowShowSurfacesDeclarationProblems(t *testing.T) {
	a := showFixture(t, "[reviewer]\nrole = \"reviewer\"\n")
	out := render(t, a, showDoc("hooks:\n  - operation: close_review\n    skill: s\n"), func(string) bool { return true })
	if !strings.Contains(out, "operation:  UNKNOWN") {
		t.Errorf("an unknown operation must be marked:\n%s", out)
	}
	if !strings.Contains(out, "PROBLEM") || !strings.Contains(out, "close_review") {
		t.Errorf("the declaration problem must be printed:\n%s", out)
	}
}

// TestWorkflowShowNoHooksSaysSo keeps the common case legible: a workflow with
// no hook declares none, and the view says what that means.
func TestWorkflowShowNoHooksSaysSo(t *testing.T) {
	a := showFixture(t, "[reviewer]\nrole = \"reviewer\"\n")
	out := render(t, a, showDoc(""), func(string) bool { return true })
	if !strings.Contains(out, "(none declared — creation stays deterministic-only)") {
		t.Errorf("missing the no-hooks line:\n%s", out)
	}
}

// A hook's binding shows where it runs (sty_7028d996): local by default, remote or
// team:<path> when the binding declares it.
func TestWorkflowShowRendersEachBindingsPlacement(t *testing.T) {
	const cmd = `command = "claude -p --disallowedTools Write,Edit --append-system-prompt {system}"` + "\n" + `tools = "Read,Grep,Glob"` + "\n"
	a := showFixture(t, "[reviewer]\nrole = \"reviewer\"\n"+cmd+
		"\n[far]\nrole = \"reviewer\"\n"+cmd+"placement = \"remote\"\nremote = \"r1\"\n"+
		"\n[team]\nrole = \"agent\"\n"+cmd+"placement = \"team:acme/ops\"\n")
	for agent, want := range map[string]string{
		"reviewer": "placement:  local",
		"far":      "placement:  remote",
		"team":     "placement:  team:acme/ops",
	} {
		doc := showDoc("hooks:\n  - operation: create_review\n    skill: s\n    agent: " + agent + "\n")
		if out := render(t, a, doc, func(string) bool { return true }); !strings.Contains(out, want) {
			t.Errorf("[%s]: want %q in:\n%s", agent, want, out)
		}
	}
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
