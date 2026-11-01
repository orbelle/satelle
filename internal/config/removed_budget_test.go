package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// sty_ba9f5d14: the machine catalog is read raw for a removed budget; an absent
// or clean catalog reports nothing, and a heal keeps every other line.
func TestFindRemovedBudgetCatalog(t *testing.T) {
	home := testutil.IsolateHome(t)
	path := filepath.Join(home, GlobalAgentsName)

	if e, err := FindRemovedBudgetCatalog(); err != nil || e != nil {
		t.Fatalf("absent catalog: edit=%+v err=%v, want nil, nil", e, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("finding an absent catalog created it: %v", err)
	}

	const clean = "[profiles.claude-coder]\ncommand = \"claude -p\"\n"
	if err := os.WriteFile(path, []byte(clean), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, err := FindRemovedBudgetCatalog(); err != nil || e != nil {
		t.Fatalf("clean catalog: edit=%+v err=%v, want nil, nil", e, err)
	}

	const dirty = "# my catalog\n[profiles.claude-coder]\ncommand = \"claude -p --output-format json --max-turns {max_turns}\"\nturn_budget = 40\nmodel = \"opus\"\n"
	if err := os.WriteFile(path, []byte(dirty), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := HealRemovedBudgetCatalog()
	if err != nil || e == nil {
		t.Fatalf("dirty catalog: edit=%+v err=%v, want an edit", e, err)
	}
	if e.Rel != GlobalAgentsLabel || !slices.Equal(e.Removed, []string{"turn_budget", RemovedMaxTurnsPlaceholder}) {
		t.Errorf("edit = %+v, want Rel %q removing turn_budget and %s", e, GlobalAgentsLabel, RemovedMaxTurnsPlaceholder)
	}
	got, _ := os.ReadFile(path)
	want := "# my catalog\n[profiles.claude-coder]\ncommand = \"claude -p --output-format json\"\nmodel = \"opus\"\n"
	if string(got) != want {
		t.Errorf("healed catalog =\n%q\nwant\n%q", got, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("heal changed the catalog mode to %v", info.Mode().Perm())
	}
	if e, err := HealRemovedBudgetCatalog(); err != nil || e != nil {
		t.Errorf("second heal: edit=%+v err=%v, want nil, nil", e, err)
	}
}
