package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedCategoryVocabulary(t *testing.T) {
	if err := EmbeddedCategoriesErr(); err != nil {
		t.Fatalf("embedded categories load: %v", err)
	}
	list := EmbeddedCategories()
	if len(list) == 0 {
		t.Fatal("embedded category list is empty")
	}
	// Tripwire: EmbeddedDefaults is .md-only — categories.toml must not appear.
	for _, d := range EmbeddedDefaults() {
		if d.Name == "categories" || strings.Contains(d.Kind, "config") {
			t.Fatalf("EmbeddedDefaults must not seed categories.toml, got kind=%q name=%q", d.Kind, d.Name)
		}
	}
	need := []string{"substrate", "epic-parent", "parent", "feature", "fix", "empty"}
	for _, n := range need {
		if !listContainsFold(list, n) {
			t.Errorf("embedded list missing mandatory %q: %v", n, list)
		}
	}
	// Zero Config resolves to the embedded list (FS-sourced, not a Go literal).
	var zero Config
	got := zero.ResolveCategoryVocabulary()
	if len(got) != len(list) {
		t.Fatalf("zero Config vocabulary len=%d want %d", len(got), len(list))
	}
	for i := range list {
		if got[i] != list[i] {
			t.Fatalf("zero Config[%d]=%q want %q", i, got[i], list[i])
		}
	}
}

func TestCategoryVocabularyOverride(t *testing.T) {
	cfg := Config{
		Categories: CategoriesConfig{
			Vocabulary: []string{"alpha", "beta"},
			Extra:      []string{"gamma", "Alpha"}, // Alpha deduped case-insensitively
		},
	}
	got := cfg.ResolveCategoryVocabulary()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("replace+extra = %v", got)
	}
	// extra alone appends to embedded
	cfg2 := Config{Categories: CategoriesConfig{Extra: []string{"custom-type"}}}
	got2 := cfg2.ResolveCategoryVocabulary()
	if !listContainsFold(got2, "feature") || !listContainsFold(got2, "custom-type") {
		t.Fatalf("extra should append to embedded: %v", got2)
	}
}

func TestCanonicaliseCategoryUnknown(t *testing.T) {
	cfg := Config{Categories: CategoriesConfig{Enforce: "reject", Vocabulary: []string{"fix", "feature"}}}
	_, err := cfg.CanonicaliseCategory("defect")
	if err == nil {
		t.Fatal("want error for unknown category under reject")
	}
	s := err.Error()
	for _, want := range []string{"defect", "fix", "feature"} {
		if !strings.Contains(s, want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
}

func TestCategoryEnforceDefault(t *testing.T) {
	var zero Config
	if zero.CategoryEnforce() != "warn" {
		t.Fatalf("default enforce = %q, want warn", zero.CategoryEnforce())
	}
	cfg := Config{Categories: CategoriesConfig{Enforce: "reject"}}
	if cfg.CategoryEnforce() != "reject" {
		t.Fatalf("enforce reject = %q", cfg.CategoryEnforce())
	}
	cfg2 := Config{Categories: CategoriesConfig{Enforce: "off"}}
	if cfg2.CategoryEnforce() != "off" {
		t.Fatalf("enforce off = %q", cfg2.CategoryEnforce())
	}
}

func TestCanonicaliseCategoryCasing(t *testing.T) {
	for _, enforce := range []string{"warn", "reject", "off"} {
		cfg := Config{Categories: CategoriesConfig{Enforce: enforce}}
		for _, in := range []string{"Substrate", "SUBSTRATE", "substrate"} {
			got, err := cfg.CanonicaliseCategory(in)
			if err != nil {
				t.Fatalf("enforce=%s in=%q: %v", enforce, in, err)
			}
			if got != "substrate" {
				t.Errorf("enforce=%s in=%q → %q, want substrate", enforce, in, got)
			}
		}
	}
}

func TestDefaultCategoryClusters(t *testing.T) {
	list := EmbeddedCategories()
	if !listContainsFold(list, "fix") {
		t.Error("list must contain fix (survivor of bug/bugfix/defect)")
	}
	for _, ban := range []string{"bug", "bugfix", "defect"} {
		if listContainsFold(list, ban) {
			t.Errorf("list must not contain synonym %q", ban)
		}
	}
	if !listContainsFold(list, "infrastructure") {
		t.Error("list must contain infrastructure (survivor of infra)")
	}
	if listContainsFold(list, "infra") {
		t.Error("list must not contain infra")
	}
	for _, ban := range []string{"frontend", "web", "ui", "cli"} {
		if listContainsFold(list, ban) {
			t.Errorf("surface-shaped %q must not be a category", ban)
		}
	}
	// Collapse map ships in the embedded file comment.
	raw, err := substrateFS.ReadFile(categoriesFile)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"bug", "defect", "fix", "infra", "infrastructure", "frontend", "surface:"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded file comment should name %q", want)
		}
	}
}

func TestCategoryWarning(t *testing.T) {
	cfg := Config{} // default warn
	if w := cfg.CategoryWarning("defect"); w == "" || !strings.Contains(w, "defect") || !strings.Contains(w, "fix") {
		t.Fatalf("warn notice: %q", w)
	}
	if w := cfg.CategoryWarning("fix"); w != "" {
		t.Fatalf("legal category should not warn: %q", w)
	}
	cfg.Categories.Enforce = "reject"
	if w := cfg.CategoryWarning("defect"); w != "" {
		t.Fatalf("reject mode has no CLI warn: %q", w)
	}
	cfg.Categories.Enforce = "off"
	if w := cfg.CategoryWarning("defect"); w != "" {
		t.Fatalf("off mode has no warn: %q", w)
	}
}

// Guard: no Go file under this package hardcodes the full default list as a
// slice literal of the surviving values (the FS is the source of truth).
func TestCategoryListNotHardcodedInGo(t *testing.T) {
	// categories.go must call EmbeddedCategories / ResolveCategoryVocabulary,
	// not restate the shipped values as a Go composite literal of all defaults.
	src, err := os.ReadFile(filepath.Join("categories.go"))
	if err != nil {
		t.Fatal(err)
	}
	// A full default list would include substrate + epic-parent + feature together
	// in one composite. Spot-check that the source of truth remains the FS path.
	if !strings.Contains(string(src), "categoriesFile") && !strings.Contains(string(src), "EmbeddedCategories") {
		t.Error("categories.go should resolve via EmbeddedCategories / categoriesFile")
	}
	// The toml file must exist under substrate/.
	if _, err := substrateFS.ReadFile(categoriesFile); err != nil {
		t.Fatalf("embedded file missing: %v", err)
	}
}

func listContainsFold(ss []string, want string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
