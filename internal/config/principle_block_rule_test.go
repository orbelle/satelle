package config

import (
	"regexp"
)

// blockRuleLead opens the session-resident rule that a block is never worked
// around (sty_902140d6).
const blockRuleLead = "**A block is never worked around"

// sentenceEnd matches a sentence terminator: a full stop, question or
// exclamation mark followed by whitespace or the end of the paragraph.
var sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)
