package config

import (
	"regexp"
	"strings"
)

// The shipped route halves are the first process file a new operator ever reads
// and edits, so their comments must earn their place — and, being embedded in
// the binary, they must do it without carrying any one repo's vocabulary
// (satelle-repo-agnostic).
//
// Three nets, deliberately opposite in direction:
//   - TestEmbeddedRouteSourcesCarryNoFormatTuition asserts the syntax teaching is
//     GONE. It used to assert the opposite (sty_58911b1a): a "HOW TO READ THIS
//     FILE" preamble was mandatory, because the format was bespoke and existed
//     nowhere else. The route source is TOML now (sty_81bb0dde), and TOML's
//     grammar is not satelle's to teach — a `[table]`, a `#` comment and a
//     quoted key need no preamble. That preamble was 50 lines across the two
//     halves, and none of it ever reached an implementing agent: the dispatch
//     path carries the DERIVED route document, never the source.
//   - TestEmbeddedRouteSourcesStateTheirModel asserts what TOML does NOT supply
//     is still stated. Syntax is self-evident; the MODEL is not. Nothing in TOML
//     says a step's key is the obligation it discharges, or that `status` is a
//     stage name that deliberately repeats. Deleting that with the tuition would
//     be the over-correction this net exists to catch — a ban-list alone would
//     pass an empty file.
//   - TestEmbeddedRouteSourcesAreRepoAgnostic asserts nothing repo-specific
//     leaked in with either.

// commentLines returns the file's comment lines, which is where all the teaching
// and all the leakage risk lives. A TOML comment is `#`.
func commentLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out
}

// preambleOf returns everything before the first record table, skipping the
// `[meta]` header the file opens with.
func preambleOf(body string) string {
	rest := body
	if i := strings.Index(body, "\n[meta]"); i >= 0 {
		rest = body[i+len("\n[meta]"):]
	} else if strings.HasPrefix(body, "[meta]") {
		rest = body[len("[meta]"):]
	}
	// The first line that opens a table other than meta ends the preamble.
	lines := strings.Split(rest, "\n")
	for i, ln := range lines {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[meta]") {
			return strings.Join(lines[:i], "\n")
		}
	}
	return rest
}

// formatTuition is syntax a reader gets from TOML itself, so a comment
// explaining it is prose the representation made unnecessary (AC4). Each entry
// is a marker of the retired bespoke grammar or of teaching TOML's own rules.
var formatTuition = map[string]string{
	"HOW TO READ THIS FILE": "the preamble the bespoke format needed; TOML needs none",
	"## ":                   "a markdown heading — records are `[table]` now",
	"<!--":                  "an HTML comment — a TOML comment is `#`",
	"one line":              "the one-line-comment convention a real parser makes unnecessary",
	"self-contained on one": "same",
}

// recordsMissingComment returns the record tables with no comment line between
// the table header and the record's first key. `[meta]` is the document header,
// not a record.
func recordsMissingComment(body string) []string {
	var missing []string
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, "[") || strings.HasPrefix(s, "[meta]") {
			continue
		}
		name := s
		commented := false
		// A banner above the table counts: it is the comment naming what the whole
		// route family below it is for.
		for j := i - 1; j >= 0; j-- {
			p := strings.TrimSpace(lines[j])
			if p == "" {
				continue
			}
			if strings.HasPrefix(p, "#") {
				commented = true
			}
			break
		}
		for j := i + 1; j < len(lines) && !commented; j++ {
			n := strings.TrimSpace(lines[j])
			if n == "" {
				continue
			}
			if strings.HasPrefix(n, "#") {
				commented = true
			}
			break // first content line reached
		}
		if !commented {
			missing = append(missing, name)
		}
	}
	return missing
}

var storyID = regexp.MustCompile(`sty_[0-9a-f]{8}`)
