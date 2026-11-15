package gate

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
)

type Verdict struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
	Class   string `json:"class,omitempty"`
}

func ParseVerdict(text string) Verdict {
	v, _ := parseVerdict(text)
	return v
}

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

var legacyAnswerKey = regexp.MustCompile("\"decision\"\\s*:|`decision`")

func LegacyAnswerKey(text string) (string, bool) {
	if legacyAnswerKey.MatchString(text) {
		return "decision", true
	}
	return "", false
}
