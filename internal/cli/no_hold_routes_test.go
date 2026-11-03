package cli

import (
	"strings"
	"testing"
)

// `satelle story hold` is no longer a command (sty_40853d02 AC2); its help topic
// is gone with it.
func TestStoryHoldCommandIsGone(t *testing.T) {
	tempRepo(t)
	for _, sub := range []string{"checkout", "release", "takeover", "show"} {
		out, err := runRoot(t, "story", "hold", sub, "sty_00000000")
		if err == nil || !strings.Contains(err.Error(), `unknown command "hold"`) {
			t.Errorf("story hold %s: err = %v, want cobra's unknown command\n%s", sub, err, out)
		}
	}
	if out, err := runRoot(t, "help", "story-hold"); err == nil && strings.Contains(out, "takeover") {
		t.Errorf("the story-hold help topic is still served:\n%s", out)
	}
}

// ledgerKindCount is how many events of kind the server holds for a story.
func (f *fakeWorkstateServer) ledgerKindCount(project, storyID, kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, raw := range f.ledgerByID[project] {
		if m, _ := raw.(map[string]any); m["story_id"] == storyID && m["kind"] == kind {
			n++
		}
	}
	return n
}
