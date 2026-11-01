package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// sty_58a7bdfe AC2/AC5: satelle init removes the removed budget keys on raw
// bytes before anything loads — [validate.injected.budget] from the committed
// config and its local overlay, context_budget / turn_budget from the agents
// layers and the route sources — leaving every other line as it was, and the
// healed repo loads. Scaffold drift names each one until then.
func TestInitRemovesRemovedBudgetKeys(t *testing.T) {
	repo := t.TempDir()
	dataDir := filepath.Join(repo, config.DefaultDataDir)
	wfDir := filepath.Join(dataDir, config.AgentsConfigDir)
	if err := os.MkdirAll(filepath.Join(wfDir, "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	const budgetTable = "[validate.injected.budget]\ndriver = 6000\nreviewer = 9000\n\n"
	shared := "# committed config\n[gate]\nmode = \"enforce\"\n\n" + budgetTable + "[sync]\nstale_after = \"6h\" # keep\n"
	local := "[validate.injected]\n" + "# local note\n\n" + budgetTable + "[harness.claude]\ncontext_limit_bytes = 9000\n"
	agents := "[defaults]\ncontext_budget = 400000\nsecondary = \"\"\n\n[coder]\n# the coder\ncommand = \"claude -p --output-format json --max-turns {max_turns} --append-system-prompt {system}\"\nturn_budget = 40\nrole = \"agent\"\n"
	steps := "[coded]\nstatus = \"in_progress\"\ncontext_budget = 9\nagent = \"coder\"\n"
	files := map[string]string{
		filepath.Join(dataDir, config.ConfigName):      shared,
		filepath.Join(dataDir, config.LocalConfigName): local,
		filepath.Join(wfDir, config.AgentsConfigName):  agents,
		filepath.Join(wfDir, "custom", "step.toml"):    steps,
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err == nil {
		t.Fatal("test premise: a config carrying the budget table must not load")
	}
	if _, err := config.LoadAgents(dataDir); err == nil {
		t.Fatal("test premise: an agents layer carrying a budget key must not load")
	}
	drift := DetectScaffoldDrift(repo)
	var named []string
	for _, f := range drift {
		if strings.Contains(f.Detail, "is removed — run satelle init") && (strings.Contains(f.Detail, "budget") || strings.Contains(f.Detail, "max_turns")) {
			named = append(named, f.Path+": "+f.Detail)
		}
	}
	if len(named) != 6 {
		t.Errorf("drift must name each removed budget (2 tables; agents.toml context_budget, turn_budget and {max_turns}; step.toml context_budget), got %d: %v", len(named), named)
	}

	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v\n%s", err, out.String())
	}
	want := map[string]string{
		filepath.Join(dataDir, config.ConfigName):      "# committed config\n[gate]\nmode = \"enforce\"\n\n[sync]\nstale_after = \"6h\" # keep\n",
		filepath.Join(dataDir, config.LocalConfigName): "[validate.injected]\n# local note\n\n[harness.claude]\ncontext_limit_bytes = 9000\n",
		filepath.Join(wfDir, "custom", "step.toml"):    "[coded]\nstatus = \"in_progress\"\nagent = \"coder\"\n",
	}
	for p, w := range want {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// Later init heals may append to the committed config; the lines it held
		// must survive byte-identical and in order, with the table gone.
		if p == filepath.Join(dataDir, config.ConfigName) {
			if !strings.HasPrefix(string(got), strings.TrimSuffix(w, "\n")) || strings.Contains(string(got), "injected.budget") {
				t.Errorf("%s =\n%s\nwant it to begin with\n%s", p, got, w)
			}
			continue
		}
		if string(got) != w {
			t.Errorf("%s =\n%q\nwant\n%q", p, got, w)
		}
	}
	gotAgents, _ := os.ReadFile(filepath.Join(wfDir, config.AgentsConfigName))
	for _, gone := range []string{"context_budget", "turn_budget", "max_turns", "max-turns"} {
		if strings.Contains(string(gotAgents), gone) {
			t.Errorf("agents.toml still carries %s:\n%s", gone, gotAgents)
		}
	}
	for _, keep := range []string{"# the coder", `command = "claude -p --output-format json --append-system-prompt {system}"`, `role = "agent"`} {
		if !strings.Contains(string(gotAgents), keep) {
			t.Errorf("agents.toml lost %q:\n%s", keep, gotAgents)
		}
	}
	if _, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err != nil {
		t.Errorf("the healed config does not load: %v", err)
	}
	if _, err := config.LoadAgents(dataDir); err != nil {
		t.Errorf("the healed agents layer does not load: %v", err)
	}
	if !strings.Contains(out.String(), "satelle manages no context or turn budget") {
		t.Errorf("init output does not report the removal:\n%s", out.String())
	}
	if left, _ := config.FindRemovedBudget(dataDir); len(left) != 0 {
		t.Errorf("init left removed budgets: %+v", left)
	}
}
