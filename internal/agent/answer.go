package agent

import (
	"encoding/json"
	"strings"
)

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
