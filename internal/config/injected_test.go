package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sty_58a7bdfe AC5: [validate.injected.budget] is removed. A satelle.toml or
// satelle.local.toml carrying it fails to load naming the table and the heal.
func TestInjectedBudgetTableRefusedAtLoad(t *testing.T) {
	for _, tc := range []struct{ name, file, body string }{
		{"committed table", ConfigName, "[validate.injected.budget]\ndriver = 100\n"},
		{"committed inline", ConfigName, "[validate.injected]\nbudget = { driver = 100 }\n"},
		{"local overlay", LocalConfigName, "[validate.injected.budget]\nreviewer = 9000\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ConfigName), []byte("[instruction_review]\nextra_paths = []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, tc.file)
			prior, _ := os.ReadFile(path)
			if err := os.WriteFile(path, append(prior, []byte("\n"+tc.body)...), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := Load(filepath.Join(dir, ConfigName))
			if err == nil {
				t.Fatal("a [validate.injected.budget] table must fail to load")
			}
			if !strings.Contains(err.Error(), RemovedInjectedBudgetTable) || !strings.Contains(err.Error(), "satelle init") {
				t.Errorf("refusal must name the table and satelle init, got %v", err)
			}
		})
	}
}

// A [validate.injected] table with no budget still loads.
func TestInjectedTableWithoutBudgetLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigName), []byte("[validate.injected]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(filepath.Join(dir, ConfigName)); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// RemoveInjectedBudget drops the table and keeps every other line as it was.
func TestRemoveInjectedBudgetKeepsOtherLines(t *testing.T) {
	in := "# head comment\n[gate]\nmode = \"enforce\"\n\n[validate.injected]\nbytes_per_token = 4\n\n[validate.injected.budget]\ndriver = 6000\nreviewer = 9000\n\n[sync]\nserver = \"x\" # tail\n"
	want := "# head comment\n[gate]\nmode = \"enforce\"\n\n[validate.injected]\nbytes_per_token = 4\n\n[sync]\nserver = \"x\" # tail\n"
	got, changed := RemoveInjectedBudget(in)
	if !changed || got != want {
		t.Errorf("RemoveInjectedBudget =\n%q\nwant\n%q", got, want)
	}
	if again, changed := RemoveInjectedBudget(got); changed || again != got {
		t.Error("a file with no budget table must be left alone")
	}
	inline := "[validate.injected]\nbudget = { driver = 1 }\nbytes_per_token = 4\n"
	if got, _ := RemoveInjectedBudget(inline); got != "[validate.injected]\nbytes_per_token = 4\n" {
		t.Errorf("inline budget not removed: %q", got)
	}
}

// RemoveBudgetKeys drops context_budget and turn_budget from every table and
// keeps every other line as it was.
func TestRemoveBudgetKeysKeepsOtherLines(t *testing.T) {
	in := "[defaults]\ncontext_budget = 100\nsecondary = \"b\"\n\n[coder]\n# keep me\ncommand = \"claude -p\"\nturn_budget = 20\n"
	want := "[defaults]\nsecondary = \"b\"\n\n[coder]\n# keep me\ncommand = \"claude -p\"\n"
	got, removed := RemoveBudgetKeys(in)
	if got != want || strings.Join(removed, ",") != "context_budget,turn_budget" {
		t.Errorf("RemoveBudgetKeys = %q %v", got, removed)
	}
	if again, removed := RemoveBudgetKeys(got); again != got || removed != nil {
		t.Error("a file with no budget key must be left alone")
	}
}

// sty_58a7bdfe AC2: context_budget and turn_budget are removed from bindings and
// [defaults]. An agents layer carrying either fails to load naming the key and
// that satelle init removes it.
func TestAgentsBudgetKeysRefusedNamingTheHeal(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"binding context", "[coder]\ncommand = \"claude -p\"\ncontext_budget = 300000\n", "context_budget"},
		{"binding turns", "[coder]\ncommand = \"claude -p\"\nturn_budget = 40\n", "turn_budget"},
		{"defaults", "[defaults]\ncontext_budget = 400000\n", "context_budget"},
		{"reviewer", "[reviewer]\nturn_budget = 5\n", "turn_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadAgentsBody(tc.body)
			if err == nil {
				t.Fatal("a budget key must fail to load")
			}
			if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "satelle init") {
				t.Errorf("refusal = %v, want it to name %s and satelle init", err, tc.key)
			}
		})
	}
	if _, err := loadAgentsBody("[coder]\ncommand = \"claude -p\"\n"); err != nil {
		t.Errorf("a binding with no budget key must load: %v", err)
	}
}

// sty_58a7bdfe AC3: init strips {max_turns} and the flag that carried it from a
// command template, keeping the rest of the line and every other line.
func TestRemoveMaxTurnsPlaceholder(t *testing.T) {
	for in, want := range map[string]string{
		"command = \"claude -p --max-turns {max_turns} --model {model}\"\n# {max_turns} in a comment\n": "command = \"claude -p --model {model}\"\n# {max_turns} in a comment\n",
		"command = \"grok -p {payload} --max-turns={max_turns}\"\n":                                     "command = \"grok -p {payload}\"\n",
		"command = \"x {max_turns} y\"\n":                                                               "command = \"x y\"\n",
	} {
		got, changed := RemoveMaxTurnsPlaceholder(in)
		if !changed || got != want {
			t.Errorf("RemoveMaxTurnsPlaceholder(%q) = %q, want %q", in, got, want)
		}
	}
	if got, changed := RemoveMaxTurnsPlaceholder("command = \"claude -p --max-turns 8\"\n"); changed || got != "command = \"claude -p --max-turns 8\"\n" {
		t.Error("an operator's own literal turn flag is the harness's configuration and is kept")
	}
}
