package cli

// sty_66abc0e3 — locked substrate is never edit-exempt. The seeded
// edit_exempt_paths entry ".satelle/" used to let an edit through when no story
// was engaged, although the same edit was refused while any story performed.
// These tests pin the lock as independent of whether anything is performing,
// through the Edit route and both Bash handlers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// lockedTarget is a locked path the edit/shell cases below all change.
const lockedTarget = ".satelle/workflows/step.toml"

// noEngagement is the sentence the no-engagement refusal opens its condition with.
const noEngagement = "no engaged story in the substrate lane"

// lockedChange is one way to change lockedTarget: an Edit through `hook gate`, or
// a shell command through each Bash handler.
type lockedChange struct {
	name     string
	handlers []string
	event    func(repo string) string
}

func lockedChanges() []lockedChange {
	bash := func(cmd string) func(string) string {
		return func(string) string { return bashEvent(cmd) }
	}
	return []lockedChange{
		{"Edit", []string{"gate"}, func(repo string) string { return claudeEditEvent(filepath.Join(repo, lockedTarget)) }},
		{"sed -i", bashHandlers, bash("sed -i s/a/b/ " + lockedTarget)},
		{"redirect", bashHandlers, bash("echo x > " + lockedTarget)},
		{"interpreter", bashHandlers, bash(`python3 -c "open('` + lockedTarget + `','w')"`)},
	}
}

// noSeatRepo is a seeded-config repo with no story engaged; extraExempt joins the
// edit_exempt_paths list.
func noSeatRepo(t *testing.T, extraExempt string) string {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	cfg := lockExemptToml
	if extraExempt != "" {
		cfg = strings.Replace(cfg, `"/tmp/"]`, `"/tmp/", "`+extraExempt+`"]`, 1)
	}
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// AC1: with nothing engaged, a change to locked substrate is refused, naming the
// substrate lane, on every route.
func TestSubstrateLockRefusesWithNoStoryEngaged(t *testing.T) {
	for _, c := range lockedChanges() {
		for _, handler := range c.handlers {
			t.Run(handler+"/"+c.name, func(t *testing.T) {
				repo := noSeatRepo(t, "")
				out, err := runRootIn(t, c.event(repo), "hook", handler)
				if err == nil {
					t.Fatalf("with no story engaged %s must be refused:\n%s", c.name, out)
				}
				reason := denyReasonOf(t, out)
				for _, want := range []string{"substrate lock", substrateLaneCategory, lockedTarget} {
					if !strings.Contains(reason, want) {
						t.Errorf("deny missing %q: %s", want, reason)
					}
				}
				if rows := ledgerRows(t, ledger.KindSubstrateLockDeny); len(rows) != 0 {
					t.Errorf("no holder is at fault, so no %s row is written, got %d", ledger.KindSubstrateLockDeny, len(rows))
				}
			})
		}
	}
}

// The refusal text is the same through every route.
func TestSubstrateLockNoEngagementTextIsRouteIndependent(t *testing.T) {
	var want string
	for _, c := range lockedChanges() {
		for _, handler := range c.handlers {
			repo := noSeatRepo(t, "")
			out, err := runRootIn(t, c.event(repo), "hook", handler)
			if err == nil {
				t.Fatalf("%s/%s must be refused:\n%s", handler, c.name, out)
			}
			got := denyReasonOf(t, out)
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("%s/%s deny differs:\n%s\n%s", handler, c.name, got, want)
			}
		}
	}
}

// AC3: an exempt path that is not locked stays exempt with nothing engaged.
func TestSubstrateLockLeavesNonLockedExemptPathsExempt(t *testing.T) {
	cases := []struct {
		name     string
		handlers []string
		event    func(repo string) string
	}{
		{"Edit", []string{"gate"}, func(repo string) string { return claudeEditEvent(filepath.Join(repo, "scratch", "x.txt")) }},
		{"redirect", bashHandlers, func(string) string { return bashEvent("echo x > scratch/x.txt") }},
		{"tmp redirect", bashHandlers, func(string) string { return bashEvent("echo x > /tmp/x") }},
		{"tmp Edit", []string{"gate"}, func(string) string { return claudeEditEvent("/tmp/satelle-noseat-scratch.md") }},
	}
	for _, c := range cases {
		name, ev := c.name, c.event
		for _, handler := range c.handlers {
			t.Run(handler+"/"+name, func(t *testing.T) {
				repo := noSeatRepo(t, "scratch/")
				if out, err := runRootIn(t, ev(repo), "hook", handler); err != nil {
					t.Fatalf("an exempt path outside the lock must stay writable with nothing engaged: %v\n%s", err, out)
				}
			})
		}
	}
}

// AC4: the refusal says how to proceed.
func TestSubstrateLockNoEngagementRefusalSaysHowToProceed(t *testing.T) {
	for _, c := range lockedChanges() {
		for _, handler := range c.handlers {
			t.Run(handler+"/"+c.name, func(t *testing.T) {
				repo := noSeatRepo(t, "")
				out, err := runRootIn(t, c.event(repo), "hook", handler)
				if err == nil {
					t.Fatalf("must be refused:\n%s", out)
				}
				reason := denyReasonOf(t, out)
				for _, want := range []string{
					noEngagement,
					"engage it",
					`--category ` + substrateLaneCategory,
					"If the substrate itself prevents engagement",
					"raise a story whose category route can take that path",
					"propose one to the developer and create it only with their approval",
				} {
					if !strings.Contains(reason, want) {
						t.Errorf("deny missing %q: %s", want, reason)
					}
				}
				// sty_902140d6: the refusal never sends the work around satelle.
				if strings.Contains(reason, "outside the agent session") {
					t.Errorf("deny still directs a repair outside the agent session: %s", reason)
				}
			})
		}
	}
}

// A repo satelle does not govern has no substrate to lock: the lock stays inert
// and the edit is not refused by it.
func TestSubstrateLockInertInUngovernedRepo(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	t.Chdir(repo)
	// No .satelle/ anywhere: app.Open reports ErrNotInitialised.
	out, _ := runRootIn(t, claudeEditEvent(filepath.Join(repo, lockedTarget)), "hook", "gate")
	if strings.Contains(out, "substrate lock") {
		t.Fatalf("an ungoverned repo must not be locked:\n%s", out)
	}
}

// AC5: the precedence rule is stated once, in `satelle help substrate`, and the
// hook text points at it.
func TestSubstrateLockPrecedenceDocumentedOnce(t *testing.T) {
	topic, err := runHelp(t, "help", "substrate")
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(topic), " ")
	if !strings.Contains(flat, "A path under [gate] lock_substrate_paths is never edit-exempt") {
		t.Errorf("help substrate must state the precedence rule:\n%s", topic)
	}
	hook, err := runRoot(t, "hook", "gate", "--help")
	if err != nil {
		t.Fatal(err)
	}
	hookFlat := strings.Join(strings.Fields(hook), " ")
	if !strings.Contains(hookFlat, "satelle help substrate") {
		t.Errorf("hook gate --help must point at satelle help substrate:\n%s", hook)
	}
	for _, stale := range []string{"Exemption stops at a performing story", "even a .satelle/ edit needs an engaged story"} {
		if strings.Contains(hookFlat, stale) {
			t.Errorf("hook gate --help still carries the stale statement %q", stale)
		}
	}
	if !strings.Contains(scaffoldToml, "never exempt") || !strings.Contains(scaffoldToml, "satelle help substrate") {
		t.Error("the seeded config comments must say a locked path is never exempt and point at satelle help substrate")
	}
}

// AC5: the seeded overlap of edit_exempt_paths and lock_substrate_paths is not a
// warning.
func TestValidateDoesNotWarnOnSeededLockOverlap(t *testing.T) {
	repo := t.TempDir()
	var initOut strings.Builder
	if err := runInitTest(t, &initOut, repo); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Chdir(repo)
	out, err := runRootIn(t, "", "validate")
	if err != nil {
		t.Fatalf("validate on an init-seeded config: %v\n%s", err, out)
	}
	for _, name := range []string{"edit_exempt_paths", "lock_substrate_paths"} {
		if strings.Contains(out, name) {
			t.Errorf("validate must not warn about %s on the seeded overlap:\n%s", name, out)
		}
	}
}
