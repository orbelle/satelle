//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// pi: a warn rule on git push lets the tool call run (no block), and a deny rule
// blocks it with the message. Real satelle never prints a pi warn (the cell is
// unavailable), so the second case plants an exit-0 additionalContext answer in
// the wrapper's place: wrapperVerdict must read it as an allow, never as an
// infrastructure failure, which would block a warned git push.
func TestSlowPiHandler_WarnedGitPushRunsDeniedOneBlocks(t *testing.T) {
	push := map[string]any{"command": "git push origin main"}

	t.Run("warned command runs", func(t *testing.T) {
		r := newPiRig(t)
		r.can("commitgate", "", 0) // what real commitgate prints for a pi warn
		out := r.drive(true, toolStep("bash", push))
		if _, blk := blocked(out.Results[0]); blk || out.Results[0].Threw != nil {
			t.Fatalf("a warned git push must run: %+v", out.Results[0])
		}
	})
	t.Run("an additionalContext answer is an allow", func(t *testing.T) {
		r := newPiRig(t)
		warn := string(agentcli.PreToolUseWarn("claude", "careful")) + "\n"
		script := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '" + strings.TrimSpace(warn) + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(r.repo, filepath.FromSlash(satelleHookScriptRel)), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		out := r.drive(true, toolStep("bash", push))
		if reason, blk := blocked(out.Results[0]); blk {
			t.Fatalf("an exit-0 JSON answer with no deny must not block: %s", reason)
		}
	})
	t.Run("denied command blocks with the message", func(t *testing.T) {
		r := newPiRig(t)
		r.canDeny("commitgate", "orbctl apply changes shared state")
		out := r.drive(true, toolStep("bash", map[string]any{"command": "orbctl apply -f x"}))
		reason, blk := blocked(out.Results[0])
		if !blk || reason != "orbctl apply changes shared state" {
			t.Fatalf("deny must block with the rule's message: %+v", out.Results[0])
		}
	})
}
