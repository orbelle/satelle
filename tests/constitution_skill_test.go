//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConstitutionSkillReadsThePromptSection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", ".satelle", "skills", "satelle-constitution-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"Rule files for this judgement", "Answer with your judgement", "item text", "copies in", "outside its copies folder", "- none", "no live"} {
		if !strings.Contains(s, want) {
			t.Errorf("the constitution skill does not say %q", want)
		}
	}
	if strings.Contains(s, "outside .satelle/") || strings.Contains(s, "read the shared ones") {
		t.Error("the constitution skill still tests paths against .satelle/ or reads shared files beside the block")
	}
	if !strings.Contains(s, "no such final block") || !strings.Contains(s, "`.satelle/constitution.md` and every file in `.satelle/principles/`") {
		t.Error("the constitution skill no longer reads the shared rule files when the prompt has no rule-files block")
	}
}
