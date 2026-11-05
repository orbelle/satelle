package config

import (
	"strings"
)

// storyShapeRules are the three story-shape rules (sty_bcd344fd AC3), each as
// the phrases a review body must carry together: the card is a behaviour, every
// AC traces to it and is provable at its gate, and the slice is the smallest
// that proves it. Each rule names the principle it enforces. The operator-config
// test TestOperatorReadyReviewEnforcesStoryShape (tests/) holds this repo's
// ready-review override to the same phrases; it cannot import a _test table.
var storyShapeRules = map[string][]string{
	"card is a behaviour":           {"[[satelle-story-shape]]", "not a behaviour"},
	"AC traceable and provable":     {"traceable", "provable at"},
	"smallest slice that proves it": {"[[satelle-start-small]]", "smallest slice"},
}

// missingStoryShapeRules lists each rule whose phrases body does not carry,
// with line wrapping ignored.
func missingStoryShapeRules(body string) []string {
	flat := strings.Join(strings.Fields(body), " ")
	var missing []string
	for rule, phrases := range storyShapeRules {
		for _, p := range phrases {
			if !strings.Contains(flat, p) {
				missing = append(missing, rule+": "+p)
			}
		}
	}
	return missing
}
