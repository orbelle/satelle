package config

import (
	"strings"
	"testing"
)

// TestEmptyCategoryReviewRules (sty_42a41851): the create, amend, cancel and
// blocked reviews each state that an empty story's emptiness is not grounds to
// object, and the create review accepts trivially satisfiable criteria and
// refuses an empty story that claims product behaviour.
func TestEmptyCategoryReviewRules(t *testing.T) {
	bodies := map[string]string{}
	for _, d := range EmbeddedDefaults() {
		if d.Kind == "skills" {
			bodies[d.Name] = strings.Join(strings.Fields(d.Body), " ")
		}
	}
	shared := []string{"category: empty", "Its emptiness is not grounds to object"}
	want := map[string][]string{
		"satelle-story-create-review": {
			"**Accept** an empty story whose criteria are minimal and trivially satisfiable",
			"**Reject** an empty story whose body or criteria claim product behaviour",
		},
		"satelle-story-amend-review":   nil,
		"satelle-story-cancel-review":  nil,
		"satelle-story-blocked-review": nil,
	}
	for name, extra := range want {
		body, ok := bodies[name]
		if !ok {
			t.Errorf("embedded skill %s missing", name)
			continue
		}
		for _, phrase := range append(append([]string(nil), shared...), extra...) {
			if !strings.Contains(body, phrase) {
				t.Errorf("%s missing empty-category rule phrase %q", name, phrase)
			}
		}
	}
}
