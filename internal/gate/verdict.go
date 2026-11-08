package gate

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
)

// Verdict is what a judge returns: accept or reject, with notes, and
// optionally the judge's own short label for its blocking finding.
type Verdict struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
	Class   string `json:"class,omitempty"`
}

// ParseVerdict finds the last JSON object in text whose verdict is accept or
// reject. Without one the verdict is a reject whose notes say why.
func ParseVerdict(text string) Verdict {
	v, _ := parseVerdict(text)
	return v
}

// parseVerdict is ParseVerdict, also reporting whether the judge answered with
// a verdict (true) or the reject is the fallback for a missing one (false).
func parseVerdict(text string) (Verdict, bool) {
	var v Verdict
	if _, ok := agent.LastObject(text, func(raw json.RawMessage) bool {
		var c Verdict
		if json.Unmarshal(raw, &c) != nil || (c.Verdict != Accept && c.Verdict != Reject) {
			return false
		}
		v = c
		return true
	}); ok {
		return v, true
	}
	snippet := strings.TrimSpace(text)
	if len(snippet) > 500 {
		snippet = snippet[len(snippet)-500:]
	}
	return Verdict{Verdict: Reject, Notes: `no {"verdict":"accept"|"reject","notes":"..."} object in the judge's answer: ` + snippet}, false
}

// legacyAnswerKey matches the retired judge answer key: "decision" as a JSON
// key, or `decision` as a code span. ParseVerdict reads only verdict, so an
// answer keyed decision is always a reject.
var legacyAnswerKey = regexp.MustCompile("\"decision\"\\s*:|`decision`")

// LegacyAnswerKey reports whether text (a judge skill's body) names the
// retired answer key, and which key it is.
func LegacyAnswerKey(text string) (string, bool) {
	if legacyAnswerKey.MatchString(text) {
		return "decision", true
	}
	return "", false
}
