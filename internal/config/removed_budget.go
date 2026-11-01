package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Satelle manages no context or token budget (sty_58a7bdfe): each agent CLI
// manages its own context, so the spend bounds a repo could once configure are
// removed. Loading a file that still carries one fails naming the key, and
// satelle init removes it (RemoveBudgetKeys, RemoveInjectedBudget) — the same
// refuse-and-heal shape RemovedSyncKeys uses.

// RemovedBudgetKeys are the binding, [defaults] and workflow-step keys that once
// set a context or turn budget.
var RemovedBudgetKeys = []string{"context_budget", "turn_budget"}

// RemovedInjectedBudgetTable is the satelle.toml table that once set a per-seat
// injected-context budget.
const RemovedInjectedBudgetTable = "validate.injected.budget"

// budgetHeal is the command that removes every removed budget key from a tree.
const budgetHeal = "satelle init"

// RemovedBudgetKeyError is the refusal for one removed budget key in file, at
// where (a table name or step), naming the heal.
func RemovedBudgetKeyError(file, where, key string) error {
	return fmt.Errorf("%s: %s %s is removed — satelle manages no context or turn budget (each agent CLI manages its own); run %q to remove it",
		file, where, key, budgetHeal)
}

// RemovedInjectedBudgetError is the refusal for a [validate.injected.budget]
// table.
func RemovedInjectedBudgetError() error {
	return fmt.Errorf("[%s] is removed — satelle fits no seat's principles to a budget (each agent CLI manages its own context); run %q to remove it",
		RemovedInjectedBudgetTable, budgetHeal)
}

// IsRemovedBudgetKey reports whether k is one of RemovedBudgetKeys.
func IsRemovedBudgetKey(k string) bool {
	for _, r := range RemovedBudgetKeys {
		if r == k {
			return true
		}
	}
	return false
}

// removedInjectedBudget is the decode target of [validate.injected.budget]: any
// value there is refused.
type removedInjectedBudget struct{}

// UnmarshalTOML implements toml.Unmarshaler.
func (*removedInjectedBudget) UnmarshalTOML(any) error { return RemovedInjectedBudgetError() }

// RemoveBudgetKeys removes every RemovedBudgetKeys assignment from a TOML
// document, in any table, returning the edited text and the keys removed (in
// RemovedBudgetKeys order, each once). Every other line is kept as it was.
func RemoveBudgetKeys(s string) (string, []string) {
	lines := strings.Split(s, "\n")
	out := lines[:0:0]
	seen := map[string]bool{}
	for _, l := range lines {
		drop := false
		for _, k := range RemovedBudgetKeys {
			if isKeyLine(l, k) {
				drop, seen[k] = true, true
				break
			}
		}
		if !drop {
			out = append(out, l)
		}
	}
	var removed []string
	for _, k := range RemovedBudgetKeys {
		if seen[k] {
			removed = append(removed, k)
		}
	}
	if len(removed) == 0 {
		return s, nil
	}
	return strings.Join(out, "\n"), removed
}

// RemovedMaxTurnsPlaceholder is the command-template placeholder that once
// carried a repo's turn budget to the harness.
const RemovedMaxTurnsPlaceholder = "{max_turns}"

// maxTurnsArgRE matches {max_turns} in a command template together with the
// flag directly before it (`--max-turns {max_turns}`, `--max-turns={max_turns}`)
// or, with no flag, the bare placeholder and the space before it.
var maxTurnsArgRE = regexp.MustCompile(`(?:\s+-{1,2}[A-Za-z0-9_-]+(?:=|\s+))?\{max_turns\}|\s*\{max_turns\}`)

// RemoveMaxTurnsPlaceholder removes every {max_turns} (and the flag that
// carried it) from a TOML document's command templates, reporting whether it did.
// Every line without the placeholder is kept as it was.
func RemoveMaxTurnsPlaceholder(s string) (string, bool) {
	if !strings.Contains(s, RemovedMaxTurnsPlaceholder) {
		return s, false
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.Contains(l, RemovedMaxTurnsPlaceholder) && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = maxTurnsArgRE.ReplaceAllString(l, "")
		}
	}
	next := strings.Join(lines, "\n")
	return next, next != s
}

// RemoveInjectedBudget removes the [validate.injected.budget] table (or an
// inline budget = {…} under [validate.injected]) from a TOML document,
// reporting whether it did. Every other line is kept as it was.
func RemoveInjectedBudget(s string) (string, bool) {
	next := RemoveSection(s, RemovedInjectedBudgetTable)
	next = RemoveKey(next, "validate.injected", "budget")
	return next, next != s
}

// RemovedBudgetEdit is one file a removed budget key or table was found in:
// its data-dir-relative slash path, what was found, and the healed text.
type RemovedBudgetEdit struct {
	Rel     string
	Removed []string // "context_budget", "turn_budget", "[validate.injected.budget]"
	path    string
	healed  string
}

// FindRemovedBudget reports every file under dataDir that still carries a
// removed budget: the committed config and its local overlay for the
// [validate.injected.budget] table, and every .toml under workflows/ (the
// agents layers and the route sources) for context_budget and turn_budget. It
// reads raw bytes and never loads a config, whose load refuses them. Sorted by
// path; a missing file contributes nothing.
func FindRemovedBudget(dataDir string) ([]RemovedBudgetEdit, error) {
	var out []RemovedBudgetEdit
	for _, name := range []string{ConfigName, LocalConfigName} {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if healed, changed := RemoveInjectedBudget(string(b)); changed {
			out = append(out, RemovedBudgetEdit{Rel: name, Removed: []string{"[" + RemovedInjectedBudgetTable + "]"},
				path: filepath.Join(dataDir, name), healed: healed})
		}
	}
	root := filepath.Join(dataDir, AgentsConfigDir)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".toml" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if healed, removed := removeBindingBudget(string(b)); len(removed) > 0 {
			rel, _ := filepath.Rel(dataDir, path)
			out = append(out, RemovedBudgetEdit{Rel: filepath.ToSlash(rel), Removed: removed, path: path, healed: healed})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// removeBindingBudget removes context_budget, turn_budget and {max_turns} (with
// the flag that carried it) from a document of agent bindings, returning the
// healed text and what it removed.
func removeBindingBudget(s string) (string, []string) {
	healed, removed := RemoveBudgetKeys(s)
	if next, changed := RemoveMaxTurnsPlaceholder(healed); changed {
		healed, removed = next, append(removed, RemovedMaxTurnsPlaceholder)
	}
	return healed, removed
}

// HealRemovedBudget removes every removed budget FindRemovedBudget reports,
// keeping every other line of each file as it was, and returns what it removed.
func HealRemovedBudget(dataDir string) ([]RemovedBudgetEdit, error) {
	edits, err := FindRemovedBudget(dataDir)
	if err != nil {
		return nil, err
	}
	return edits, writeRemovedBudget(edits)
}

// FindRemovedBudgetCatalog reports the machine profile catalog
// (GlobalAgentsPath) when it still carries a removed budget: a seat bound to one
// of its profiles resolves the removed key into every repo's binding, so the
// catalog is healed with the repo files (sty_ba9f5d14). It reads raw bytes, never
// LoadGlobalAgents. Rel is GlobalAgentsLabel; an absent or clean catalog is nil.
func FindRemovedBudgetCatalog() (*RemovedBudgetEdit, error) {
	path := GlobalAgentsPath()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	healed, removed := removeBindingBudget(string(b))
	if len(removed) == 0 {
		return nil, nil
	}
	return &RemovedBudgetEdit{Rel: GlobalAgentsLabel, Removed: removed, path: path, healed: healed}, nil
}

// HealRemovedBudgetCatalog removes what FindRemovedBudgetCatalog reports,
// keeping every other line of the catalog as it was, and returns the edit (nil
// when there was nothing to remove).
func HealRemovedBudgetCatalog() (*RemovedBudgetEdit, error) {
	e, err := FindRemovedBudgetCatalog()
	if err != nil || e == nil {
		return nil, err
	}
	return e, writeRemovedBudget([]RemovedBudgetEdit{*e})
}

// writeRemovedBudget writes each edit's healed text back, keeping its file mode.
func writeRemovedBudget(edits []RemovedBudgetEdit) error {
	for _, e := range edits {
		info, err := os.Stat(e.path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(e.path, []byte(e.healed), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
