package gate

import (
	"path"
	"strings"
)

// Match reports whether a slash-separated path matches a glob in which "**"
// stands for zero or more whole segments and every other segment follows
// path.Match.
func Match(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// MatchAny reports whether any path matches any pattern.
func MatchAny(patterns, paths []string) bool {
	for _, p := range paths {
		for _, g := range patterns {
			if Match(g, p) {
				return true
			}
		}
	}
	return false
}
