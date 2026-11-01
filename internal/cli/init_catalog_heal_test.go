package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
)

// catalogBudgetProblems runs the agents.binding validation doctor runs
// (ValidateEffectiveLayered over the repo layers and the machine catalog) and
// returns each problem naming {max_turns}.
func catalogBudgetProblems(t *testing.T, dataDir string) []string {
	t.Helper()
	layers, err := config.LoadAgentLayers(dataDir)
	if err != nil {
		t.Fatalf("load agent layers: %v", err)
	}
	global, err := config.LoadGlobalAgents()
	if err != nil {
		t.Fatalf("load machine catalog: %v", err)
	}
	var out []string
	for _, p := range agentvalidate.ValidateEffectiveLayered(layers, global, nil, nil, nil).Problems {
		if strings.Contains(p, "command carries "+config.RemovedMaxTurnsPlaceholder) {
			out = append(out, p)
		}
	}
	return out
}

// catalogDrift returns the removed-budget drift findings that name the catalog.
func catalogDrift(repo string) []ScaffoldFinding {
	var out []ScaffoldFinding
	for _, f := range driftRemovedBudget(repo) {
		if f.Path == config.GlobalAgentsLabel {
			out = append(out, f)
		}
	}
	return out
}

// sty_ba9f5d14: a {max_turns} that lives only in the machine catalog's profile
// is healed by satelle init (AC1), after which the seat bound to that profile
// no longer carries it (AC2); a second init changes nothing (AC3); and the
// scaffold drift report names the catalog until init has run (AC4).
func TestInitHealsRemovedBudgetInMachineCatalog(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	dataDir := filepath.Join(repo, config.DefaultDataDir)
	wfDir := filepath.Join(dataDir, config.AgentsConfigDir)
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The repo's only reference to the removed placeholder is through the profile.
	repoAgents := "[coder]\nprofile = \"claude-coder\"\nrole = \"agent\"\ntools = \"Read\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(repoAgents), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := config.GlobalAgentsPath()
	const before = "# machine catalog\n[profiles.claude-coder]\ncommand = \"claude -p --output-format json --max-turns {max_turns} --append-system-prompt {system}\" # coder\nmodel = \"opus\"\n"
	const after = "# machine catalog\n[profiles.claude-coder]\ncommand = \"claude -p --output-format json --append-system-prompt {system}\" # coder\nmodel = \"opus\"\n"
	if err := os.WriteFile(catalog, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	// Premise: the binding check refuses the seat, naming init as the heal.
	if got := catalogBudgetProblems(t, dataDir); len(got) == 0 || !strings.Contains(got[0], "[coder]") || !strings.Contains(got[0], "run satelle init") {
		t.Fatalf("premise: problems = %v, want [coder] command carries {max_turns} … run satelle init", got)
	}
	// AC4 (before): drift names the catalog.
	drift := catalogDrift(repo)
	if len(drift) != 1 || drift[0].Detail != config.RemovedMaxTurnsPlaceholder+" is removed — run satelle init" {
		t.Fatalf("drift before init = %+v, want one %s finding: %q", drift, config.GlobalAgentsLabel, config.RemovedMaxTurnsPlaceholder+" is removed — run satelle init")
	}

	// AC1: init removes the flag and placeholder and reports the catalog line.
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	got, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != after {
		t.Errorf("catalog after init =\n%q\nwant\n%q", got, after)
	}
	wantLine := "~ " + config.GlobalAgentsLabel + " (removed " + config.RemovedMaxTurnsPlaceholder + " — satelle manages no context or turn budget)"
	if !strings.Contains(out.String(), wantLine) {
		t.Errorf("init output lacks %q:\n%s", wantLine, out.String())
	}

	// AC2: the seat bound to the profile no longer carries {max_turns}.
	if got := catalogBudgetProblems(t, dataDir); len(got) != 0 {
		t.Errorf("after init the binding check still reports: %v", got)
	}
	// AC4 (after): drift names nothing for the catalog.
	if drift := catalogDrift(repo); len(drift) != 0 {
		t.Errorf("drift after init = %+v, want none for %s", drift, config.GlobalAgentsLabel)
	}

	// AC3: a second init changes no byte and prints no catalog line.
	out.Reset()
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("second runInit: %v\n%s", err, out.String())
	}
	if again, _ := os.ReadFile(catalog); string(again) != after {
		t.Errorf("second init changed the catalog:\n%q", again)
	}
	if strings.Contains(out.String(), config.GlobalAgentsLabel) {
		t.Errorf("second init printed a catalog line:\n%s", out.String())
	}
}

// sty_ba9f5d14 AC3: with no machine catalog, init neither creates nor reports it.
func TestInitLeavesAbsentMachineCatalogAbsent(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	catalog := config.GlobalAgentsPath()
	if _, err := os.Stat(catalog); !os.IsNotExist(err) {
		t.Fatalf("premise: catalog present before init: %v", err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(catalog); !os.IsNotExist(err) {
		t.Errorf("init created the machine catalog: %v", err)
	}
	if strings.Contains(out.String(), config.GlobalAgentsLabel) {
		t.Errorf("init reported an absent catalog:\n%s", out.String())
	}
	if drift := catalogDrift(repo); len(drift) != 0 {
		t.Errorf("drift names an absent catalog: %+v", drift)
	}
}
