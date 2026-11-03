package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// A warn rule's answer must get through the generated satelle-hook.sh: without
// the structured_warn arm an exit-0 answer that is not a deny reached docase,
// which printed nothing, or turned a warned git commit/push into the infra deny.
func TestHookScriptWarnPassThrough(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, harness, command, fakeOut string, fakeCode int) (int, string) {
		t.Helper()
		home := t.TempDir()
		wrapperFakeSatelle(t, home, wrapperRow{fake: "y", out: fakeOut, code: fakeCode})
		code, stdout, _ := runHookScript(t, repo, renderHookCommand(repo, harness, "commitgate"), hookBashEvent(harness, command), home)
		return code, stdout
	}

	for _, command := range []string{"orbctl scale web 2", "git push origin main", "git commit -m x"} {
		t.Run("claude warn: "+command, func(t *testing.T) {
			warn := string(agentcli.PreToolUseWarn("claude", "careful")) + "\n"
			code, stdout := run(t, "claude", command, warn, 0)
			if code != 0 || stdout != warn {
				t.Fatalf("the warn must reach the harness untouched: code=%d stdout=%q want %q", code, stdout, warn)
			}
			if strings.Contains(stdout, `"deny"`) {
				t.Fatalf("a warned command is not denied: %q", stdout)
			}
		})
	}

	t.Run("claude deny still passes through", func(t *testing.T) {
		deny := string(agentcli.PreToolUseDeny("claude", "policy R")) + "\n"
		if _, stdout := run(t, "claude", "orbctl apply", deny, 1); stdout != deny {
			t.Fatalf("stdout = %q", stdout)
		}
	})
	t.Run("unmatched command prints nothing", func(t *testing.T) {
		if code, stdout := run(t, "claude", "ls", "", 0); code != 0 || stdout != "" {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})
	// grok, pi and cursor record no PreToolUse warn channel, so commitgate prints
	// nothing for a warned command; the wrapper must let it, git push included,
	// run without an infra deny.
	for _, harness := range []string{"grok", "pi", "cursor"} {
		for _, command := range []string{"orbctl scale web 2", "git push origin main"} {
			t.Run(harness+" warned: "+command, func(t *testing.T) {
				if code, stdout := run(t, harness, command, "", 0); code != 0 || stdout != "" {
					t.Fatalf("a warned command runs on %s: code=%d stdout=%q", harness, code, stdout)
				}
			})
		}
	}
}

// The wrapper only recognises a warn shape where the adapter records one.
func TestHookWrapperWarnArmsComeFromTheAdapter(t *testing.T) {
	body := parameterizedHookScriptBody()
	for _, h := range agentcli.HookWarnHarnesses() {
		if !agentcli.FactsFor(h).PreToolUseWarn.Available {
			t.Errorf("%s has a wrapper warn arm but its facts cell is unavailable", h)
		}
		if !strings.Contains(body, agentcli.PreToolUseWarnMatch(h)) {
			t.Errorf("the wrapper lacks %s's warn glob", h)
		}
	}
	for _, f := range agentcli.HarnessFactsTable() {
		if f.PreToolUseWarn.Available && agentcli.PreToolUseWarnMatch(f.Harness) == "" {
			t.Errorf("%s delivers a warn but the wrapper cannot recognise it", f.Harness)
		}
	}
}
