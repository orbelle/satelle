//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC6: cursor runs a repo's Claude-format hooks as well as its own, so a repo with
// both scaffolds fires the gate twice per call. The pair of verbs the Claude
// scaffold routes (gate for Write, commitgate for Shell) decide the same under
// --harness claude and --harness cursor, with nothing engaged and with an
// executor story; the native path alone also covers Delete and an unknown tool;
// and each deny is encoded for the invocation's --harness.
func TestSlowCursorAndClaudeScaffoldsDecideAlike(t *testing.T) {
	cc := loadCursorCaptured(t)
	var forwarded []cursorCall
	for _, c := range cc.all {
		// The Claude matcher in the 10b/11b/11c captures is Write|Edit|Bash and
		// cursor forwarded Write and Shell to it.
		if tool := c.Tool; tool == "Write" || tool == "Shell" {
			forwarded = append(forwarded, c)
		}
	}
	if len(forwarded) < 6 {
		t.Fatalf("want every captured Write and Shell call, got %d", len(forwarded))
	}
	for _, sc := range cursorScenarios()[:2] {
		t.Run(sc.name, func(t *testing.T) {
			repo := sc.setup(t)
			for _, c := range forwarded {
				verb := verbFor(c.Tool)
				payload := c.payload(repo)
				cursorDenied, cursorOut := decideHook(t, verb, "cursor", payload)
				claudeDenied, claudeOut := decideHook(t, verb, "claude", payload)
				if cursorDenied != claudeDenied {
					t.Errorf("%s %s (%s): cursor denied=%v, claude denied=%v\ncursor: %s\nclaude: %s",
						c.Src, c.Tool, verb, cursorDenied, claudeDenied, cursorOut, claudeOut)
				}
				if cursorDenied {
					if !strings.Contains(cursorOut, `"permission":"deny"`) || strings.Contains(cursorOut, "permissionDecision") {
						t.Errorf("%s: --harness cursor deny has the wrong encoding:\n%s", c.Src, cursorOut)
					}
					if !strings.Contains(claudeOut, `"permissionDecision":"deny"`) || strings.Contains(claudeOut, `"permission":"deny"`) {
						t.Errorf("%s: --harness claude deny has the wrong encoding:\n%s", c.Src, claudeOut)
					}
				}
			}
		})
	}

	// Only the native path sees a Delete or a tool Claude's table does not name.
	t.Run("native path covers what the claude matcher misses", func(t *testing.T) {
		repo := cursorScenarios()[0].setup(t)
		for what, payload := range map[string]string{
			"Delete (10a)": cc.del.payload(repo),
			"unknown tool": cursorEnvelope("FutureTool", map[string]any{}, repo),
		} {
			if denied, out := decideHook(t, "gate", "cursor", payload); !denied {
				t.Errorf("%s: native gate allowed it:\n%s", what, out)
			}
		}
	})
}
