package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestValidateWarnsOnPriorityValue(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "tagged", "--tag", "priority:urgent", "--tag", "surface:anything")
	createID(t, "story", "create", "--title", "untagged")

	out, err := run(t, "", "validate")
	if err != nil {
		t.Fatalf("validate fails on a tag warning: %v\n%s", err, out)
	}
	if !strings.Contains(out, "configuration valid") {
		t.Errorf("validate does not say the configuration is valid:\n%s", out)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, id) && strings.Contains(line, "priority:urgent") &&
			strings.Contains(line, "priority:high") && strings.Contains(line, "priority:medium") && strings.Contains(line, "priority:low") {
			found = true
		}
	}
	if !found {
		t.Errorf("validate prints no line naming %s, priority:urgent and the three values:\n%s", id, out)
	}
	if strings.Contains(out, "surface:anything") {
		t.Errorf("validate warns on a tag with no closed values:\n%s", out)
	}

	var rep struct {
		Valid       bool                `json:"valid"`
		TagWarnings []kernel.TagWarning `json:"tag_warnings"`
	}
	js, err := run(t, "", "validate", "--json")
	if err != nil {
		t.Fatalf("validate --json: %v\n%s", err, js)
	}
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, js)
	}
	if !rep.Valid || len(rep.TagWarnings) != 1 {
		t.Fatalf("validate --json: want valid with one tag warning:\n%s", js)
	}
	if w := rep.TagWarnings[0]; w.Item != id || w.Tag != "priority:urgent" || !slices.Equal(w.Named, []string{"priority:high", "priority:medium", "priority:low"}) {
		t.Errorf("tag warning %+v", w)
	}

	must(t, "story", "set", id, "--remove-tag", "priority:urgent", "--add-tag", "priority:low")
	js = must(t, "validate", "--json")
	if !strings.Contains(js, `"tag_warnings": []`) {
		t.Errorf("after the tag becomes priority:low: want tag_warnings []:\n%s", js)
	}
}
