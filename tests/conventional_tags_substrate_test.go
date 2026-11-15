//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestConventionalTagsPrinciple(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }

	principles, err := workflow.LoadSkills(filepath.Join("..", ".satelle", "principles"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := principles["satelle-conventional-tags"]
	if !ok {
		t.Fatal("no satelle-conventional-tags principle")
	}
	if p.Meta["type"] != "principle" || p.Meta["description"] == "" {
		t.Errorf("satelle-conventional-tags: type %q, description %q", p.Meta["type"], p.Meta["description"])
	}
	body := flat(read(".satelle/principles/satelle-conventional-tags.md"))
	for _, want := range []string{"satelle help satelle", "priority", "optional"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("the principle does not say %q", want)
		}
	}

	filing := ""
	for _, line := range strings.Split(read(".satelle/constitution.md"), "\n") {
		if strings.HasPrefix(line, "- Filing:") {
			filing = line
		}
	}
	if !strings.Contains(filing, "[[satelle-conventional-tags]]") {
		t.Errorf("the constitution's Filing line does not link the principle: %q", filing)
	}

	_, ruling, ok := strings.Cut(read(".satelle/principles/satelle-operator-rulings.md"), "\n## 2026-11-15\n")
	if !ok {
		t.Fatal("satelle-operator-rulings has no 2026-11-15 section")
	}
	ruling, _, _ = strings.Cut(ruling, "\n## ")
	for _, want := range []string{"acceptable in code", "sto_856572ef"} {
		if !strings.Contains(flat(ruling), want) {
			t.Errorf("the 2026-11-15 ruling does not hold %q", want)
		}
	}
}
