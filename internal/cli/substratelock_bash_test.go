package cli

// sty_dc77e118 — a Bash command that writes into locked substrate is held by the
// substrate lock as an Edit of that path is: the same predicate, the same deny
// text and the same ledger row, through BOTH Bash handlers (`hook gate` and
// `hook commitgate`), even though .satelle/ is exempt from the engaged-story gate.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/help"
)

// bashHandlers are the two hooks a harness may route Bash to.
var bashHandlers = []string{"gate", "commitgate"}

// editDenyReason is the reason an Edit of rel is refused with in repo — the text a
// Bash refusal of the same path must equal.
func editDenyReason(t *testing.T, repo, rel string) string {
	t.Helper()
	out, err := runRootIn(t, claudeEditEvent(filepath.Join(repo, rel)), "hook", "gate")
	if err == nil {
		t.Fatalf("the Edit control for %s must be refused:\n%s", rel, out)
	}
	return denyReasonOf(t, out)
}

// The same refusal reaches a grok-shaped Bash event: the lock reads only the
// harness-neutral command string.
func TestSubstrateLockBashGrokShapedEvent(t *testing.T) {
	lockRepo(t, "feature", "")
	// The control is the grok-shaped Edit: it carries the same session ("g1"), so
	// the lock attributes both to the same condition and the text can match.
	eOut, eErr := runRootIn(t, grokEditEvent(".satelle/skills/x.md"), "hook", "gate")
	if eErr == nil {
		t.Fatalf("the grok Edit control must be refused:\n%s", eOut)
	}
	want := denyReasonOf(t, eOut)
	ev, _ := json.Marshal(map[string]any{
		"hookEventName": "pre_tool_use", "sessionId": "g1", "toolName": "run_terminal_command",
		"toolInput": map[string]any{"command": "echo x > .satelle/skills/x.md"},
	})
	for _, handler := range bashHandlers {
		out, err := runRootIn(t, string(ev), "hook", handler)
		if err == nil {
			t.Fatalf("%s: a grok-shaped Bash write into the substrate must be refused:\n%s", handler, out)
		}
		if got := denyReasonOf(t, out); got != want {
			t.Errorf("%s: grok deny differs from the Edit's:\n%s\n%s", handler, got, want)
		}
	}
}

// The Bash gate reads the committed config once per hook call, and a command with
// nothing lockable in it records nothing.
func TestSubstrateLockBashReadsConfigOnce(t *testing.T) {
	for _, cmd := range []string{"echo x > .satelle/skills/x.md", `python3 -c "open('.satelle/x','w')"`, "git status"} {
		t.Run(cmd, func(t *testing.T) {
			lockRepo(t, "feature", "")
			n := countLockConfigReads(t, nil)
			_, _ = runRootIn(t, bashEvent(cmd), "hook", "commitgate")
			if *n != 1 {
				t.Fatalf("lock config reads = %d, want 1", *n)
			}
		})
	}
	t.Run("gate with no seat reads once and still locks", func(t *testing.T) {
		repo := tempRepo(t)
		t.Chdir(repo)
		if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(lockExemptToml), 0o644); err != nil {
			t.Fatal(err)
		}
		n := countLockConfigReads(t, nil)
		if out, err := runRootIn(t, bashEvent("echo x > .satelle/skills/x.md"), "hook", "gate"); err == nil {
			t.Fatalf("no seat: locked substrate must be refused (sty_66abc0e3):\n%s", out)
		}
		if *n != 1 {
			t.Fatalf("lock config reads = %d, want 1", *n)
		}
	})
}

// An unreadable config fails closed to the default lock for a Bash command too.
func TestSubstrateLockBashUnreadableConfigFailsClosed(t *testing.T) {
	lockRepo(t, "feature", "")
	countLockConfigReads(t, os.ErrPermission)
	for _, handler := range bashHandlers {
		if out, err := runRootIn(t, bashEvent("echo x > .satelle/skills/x.md"), "hook", handler); err == nil {
			t.Fatalf("%s: an unreadable config must lock the default, not open:\n%s", handler, out)
		}
	}
}

// A session that matches no seat is not thereby free of the lock, for Bash either.
func TestSubstrateLockBashHeldAgainstSessionThatMatchesNoSeat(t *testing.T) {
	repo, a, b := holdersRepo(t, "feature", "feature")
	_ = repo
	for _, handler := range bashHandlers {
		out, err := runRootIn(t, bashEvent("echo x > .satelle/skills/x.md"), "hook", handler)
		if err == nil {
			t.Fatalf("%s: a session bound to no seat must not slip the lock:\n%s", handler, out)
		}
		reason := denyReasonOf(t, out)
		for _, want := range []string{a.ID, b.ID, "hold performing seats", "substrate lock"} {
			if !strings.Contains(reason, want) {
				t.Errorf("%s: deny missing %q: %s", handler, want, reason)
			}
		}
	}
}

// The help states the rule and both documented limits (AC4).
func TestSubstrateLockBashHelpStatesLimits(t *testing.T) {
	for _, handler := range bashHandlers {
		out, err := runRoot(t, "hook", handler, "--help")
		if err != nil {
			t.Fatal(err)
		}
		flat := strings.Join(strings.Fields(out), " ")
		for _, want := range []string{"interpreter", "python3 tool.py", "'.sat'+'elle'", "only READS"} {
			if !strings.Contains(flat, want) {
				t.Errorf("hook %s --help missing %q", handler, want)
			}
		}
	}
}

// sty_ca667036 AC6: the help says a shell -c program is classified as shell, and
// the topic it points at names what is still text-scanned.
func TestSubstrateLockBashHelpShellProgram(t *testing.T) {
	for handler, wants := range map[string][]string{
		"gate":       {"shell -c program is classified as shell unless not plain", "satelle help substrate"},
		"commitgate": {"shell -c program is classified as shell"},
	} {
		out, err := runRoot(t, "hook", handler, "--help")
		if err != nil {
			t.Fatal(err)
		}
		flat := strings.Join(strings.Fields(out), " ")
		for _, want := range wants {
			if !strings.Contains(flat, want) {
				t.Errorf("hook %s --help missing %q", handler, want)
			}
		}
	}
	topic, ok := help.Get("substrate")
	if !ok {
		t.Fatal("help topic substrate missing")
	}
	flat := strings.Join(strings.Fields(topic.Body), " ")
	for _, want := range []string{
		"A shell -c program is classified as shell",
		"Only non-shell interpreter text (python, node, perl, ruby) is text-scanned",
		"text-scanned like it when it is not plain",
		"NAME=value words and positional arguments are always text-scanned",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("help substrate missing %q", want)
		}
	}
}
