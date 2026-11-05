package gate

import (
	"encoding/json"
	"strings"
)

// Verdict is what a judge returns: accept or reject, with notes.
type Verdict struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
}

// ParseVerdict finds the last JSON object in text whose verdict is accept or
// reject. Without one the verdict is a reject whose notes say why.
func ParseVerdict(text string) Verdict {
	var found *Verdict
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var v Verdict
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&v) == nil && (v.Verdict == Accept || v.Verdict == Reject) {
			found = &v
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	if found != nil {
		return *found
	}
	snippet := strings.TrimSpace(text)
	if len(snippet) > 500 {
		snippet = snippet[len(snippet)-500:]
	}
	return Verdict{Verdict: Reject, Notes: `no {"verdict":"accept"|"reject","notes":"..."} object in the judge's answer: ` + snippet}
}
