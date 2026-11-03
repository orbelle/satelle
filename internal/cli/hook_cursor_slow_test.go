//go:build clislow

package cli

import (
	"path/filepath"
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

// AC4/AC5: what the two verbs decide on real cursor payloads, in each state.
func TestSlowCursorGateDecisionsOnCapturedPayloads(t *testing.T) {
	cc := loadCursorCaptured(t)
	future := func(repo string) string {
		return cursorEnvelope("FutureTool", map[string]any{"file_path": filepath.Join(repo, "x.txt")}, repo)
	}
	gitShell := func(cmd string) cursorCall {
		return cursorCall{Tool: cc.plainShell.Tool, Input: map[string]any{"command": cmd, "cwd": "", "timeout": float64(30000)}}
	}
	for _, sc := range cursorScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			repo := sc.setup(t)
			type tc struct {
				what, verb, payload string
				mutates             bool // denied unless the state permits edits
				alwaysAllowed       bool
			}
			cases := []tc{
				{"Write (in-place edit)", "gate", cc.write.payload(repo), true, false},
				{"Delete", "gate", cc.del.payload(repo), true, false},
				{"unknown tool", "gate", future(repo), true, false},
				{"Shell rm", "commitgate", cc.rmShell.payload(repo), true, false},
				{"Shell sed -i", "commitgate", cc.sedShell.payload(repo), true, false},
				{"Shell sed -i && rm", "commitgate", cc.rmSedShell.payload(repo), true, false},
				{"Read via gate", "gate", cc.read.payload(repo), false, true},
				{"Grep via gate", "gate", cc.grep.payload(repo), false, true},
				{"Read via commitgate", "commitgate", cc.read.payload(repo), false, true},
				{"non-mutating Shell via commitgate", "commitgate", cc.plainShell.payload(repo), false, true},
				{"non-mutating Shell via gate", "gate", cc.plainShell.payload(repo), false, true},
				{"Write via commitgate", "commitgate", cc.write.payload(repo), false, true},
			}
			for _, c := range cases {
				denied, out := decideHook(t, c.verb, "cursor", c.payload)
				want := c.mutates && !sc.allowMutation
				if denied != want {
					t.Errorf("%s: denied=%v, want %v\n%s", c.what, denied, want, out)
				}
				if denied {
					if !strings.Contains(out, `"permission":"deny"`) || !strings.Contains(out, `"user_message"`) {
						t.Errorf("%s: deny is not cursor's encoding:\n%s", c.what, out)
					}
					if strings.Contains(out, "hookSpecificOutput") {
						t.Errorf("%s: cursor deny carries Claude's shape:\n%s", c.what, out)
					}
				}
			}
			// git commit / push are the commit gate's: refused with nothing engaged.
			for _, git := range []string{"git commit -am x", "git push origin HEAD"} {
				denied, out := decideHook(t, "commitgate", "cursor", gitShell(git).payload(repo))
				engaged := sc.name != "no story engaged"
				if denied == engaged {
					t.Errorf("%s: denied=%v with engaged=%v\n%s", git, denied, engaged, out)
				}
				if denied && !denyReasonContains(out, noEngagedStoryCommitReason) {
					t.Errorf("%s: deny is not satelle's commit reason:\n%s", git, out)
				}
			}
		})
	}
}
