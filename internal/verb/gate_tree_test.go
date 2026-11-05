package verb_test

import (
	"testing"
)

// sty_ab93f9a6: a gate's functional check runs in the MAIN tree; `story diff`
// resolves the story's own tree only for the story the gate names, and only
// inside the same repository. Every other foreign-tree diff is still refused.

const foreignTreeRefusal = "was engaged from working tree"

// diffErr runs story-diff for id from the current directory.
func diffErr(t *testing.T, id string) (string, error) {
	t.Helper()
	out, err := dispatchRaw(t, "story-diff", map[string]any{"id": id, "include_substrate": true})
	return string(out), err
}
