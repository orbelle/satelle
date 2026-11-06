package upgrade

import (
	"regexp"
	"strings"
)

// criterionMarker opens a criterion: "1." "2)" "-" or "*" at a line's start.
var criterionMarker = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*])\s+`)

// SplitCriteria turns a pre-1.0 criteria text (one string) into a list: each
// marked line opens a criterion, following unmarked lines join it, text
// before the first marker is a criterion of its own, and text with no marker
// is one criterion. Blank text gives none.
func SplitCriteria(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	cur := ""
	open := false
	flush := func() {
		if s := strings.TrimSpace(cur); s != "" {
			out = append(out, s)
		}
		cur, open = "", false
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if loc := criterionMarker.FindStringIndex(line); loc != nil {
			flush()
			cur, open = line[loc[1]:], true
			continue
		}
		if strings.TrimSpace(line) == "" && !open {
			continue
		}
		if cur != "" {
			cur += "\n"
		}
		cur += line
	}
	flush()
	return out
}
