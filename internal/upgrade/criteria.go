package upgrade

import (
	"regexp"
	"strings"
)

var criterionMarker = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*])\s+`)

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
