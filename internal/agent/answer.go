package agent

import (
	"encoding/json"
	"strings"
)

// LastObject finds the last JSON object in an agent's answer that keep
// accepts. It tries a decode at every '{', so prose, code fences and earlier
// example objects around the answer do not matter. It is the one place an
// answer's final object is found (verdicts and artifacts alike).
func LastObject(text string, keep func(json.RawMessage) bool) (json.RawMessage, bool) {
	var found json.RawMessage
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var raw json.RawMessage
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&raw) == nil && keep(raw) {
			found = raw
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return found, found != nil
}
