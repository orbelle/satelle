package cli

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
)

// Verb help is the ONLY description of a command an agent can read at runtime
// (sty_a499e7f5). A Short says what the verb is called; the Long has to answer
// the two questions that cost a turn when unanswered — when this verb is the
// right one, and the constraint that is not guessable from the flags.
//
// Two rules, both enforced here so the surface cannot regress the next time a
// command is added:
//
//	presence — every command carries a Long
//	brevity  — that Long stays inside a ceiling, because a help string that
//	           grows into an essay spends the tokens the Short was saving
//
// ENUMERATION RULE, pinned deliberately: group parents ARE included (an agent
// reads `satelle story --help` to choose a verb, so the parent is high-traffic),
// while `completion` (cobra-generated), `help` (cobra's own) and Hidden commands
// are not.

const (
	// longWordCeiling holds a Long to the three-part shape: purpose, when to
	// reach for it, the one non-obvious constraint. That fits in 60-90 words;
	// the ceiling leaves headroom for a flag caveat and nothing more.
	longWordCeiling = 130
	// longLineCeiling keeps a Long scannable in a terminal.
	longLineCeiling = 18
)

// longWordWaiver grandfathers the commands whose Long is genuinely operational
// documentation — a destructive path, a heal path, or a protocol an operator
// runs by hand — measured when the ceiling was introduced. EXACT match, so a
// waived command can only shrink (update the number down); growing one fails
// the test just as a new essay would.
var longWordWaiver = map[string]int{
	// Machine-invoked hook handlers: an operator reads these to understand what
	// the harness does on their behalf, and the decision tables ARE the content.
	"satelle hook gate":       411, // every deny reason the edit gate can return, and its remedy
	"satelle hook commitgate": 159, // the commit/push fence and the engagement it requires
	"satelle hook prompt":     135, // what the per-prompt reminder injects and when
	// Operator procedures: destructive, converging, or recovery paths where the
	// steps and the refusals are the documentation.
	"satelle migrate":         420, // structural convergence per area, with the refusal paths
	"satelle agents":          234, // per-harness launcher + hook install matrix
	"satelle workspace add":   284, // registration + machine-scope seed target
	"satelle doctor":          280, // the readiness checklist it runs, item by item
	"satelle init":            269, // what the scaffold writes and what it leaves alone
	"satelle rebase":          257, // DESTRUCTIVE: backup, reset, what is not recoverable
	"satelle sync":            204, // the four areas and the personal/team scoping rules
	"satelle service install": 189, // user vs system unit, lingering, the sudo boundary
	"satelle sync rehydrate":  152, // recovery protocol from a hosted project
	"satelle update":          172, // published-asset install plus the service cycle it performs
	"satelle runtime reap":    169, // what it deletes and what it refuses to touch
	"satelle reindex":         157, // normalisation + view regeneration, and what it never rewrites
	"satelle runtime migrate": 152, // moving the runtime plane, and the refusals that protect it
	"satelle restore":         150, // OVERWRITES drifted copies — the blast radius is the content
}

// walkCommands visits every command under root with its full invocation path,
// applying the enumeration rule above. One traversal definition, shared by the
// presence and brevity tests.
func walkCommands(root *cobra.Command, visit func(path string, c *cobra.Command)) {
	var rec func(prefix string, c *cobra.Command)
	rec = func(prefix string, c *cobra.Command) {
		for _, sub := range c.Commands() {
			name := sub.Name()
			if name == "completion" || name == "help" || sub.Hidden {
				continue
			}
			path := prefix + " " + name
			visit(path, sub)
			rec(path, sub)
		}
	}
	rec(root.Name(), root)
}

// bareCommands returns every command under root carrying no Long. Split from the
// test so the same rule can be run against a fixture tree (TestCoverageCatchesANewBareCommand).
func bareCommands(root *cobra.Command) []string {
	var bare []string
	walkCommands(root, func(path string, c *cobra.Command) {
		if strings.TrimSpace(c.Long) == "" {
			bare = append(bare, path)
		}
	})
	return bare
}

// TestEveryCommandHasLong: presence. A command with only a Short tells an agent
// what it is called and nothing about when to use it.
func TestEveryCommandHasLong(t *testing.T) {
	bare := bareCommands(NewRootCmd())
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d command(s) carry no Long — an agent reading --help learns only the name:\n  %s",
			len(bare), strings.Join(bare, "\n  "))
	}
}

// TestLongIsBrief: brevity. The ceiling is the half of the contract that stops
// "every command documented" from becoming 130 restatements of the process
// guides — those live in `satelle help <topic>`, and a Long may name one.
func TestLongIsBrief(t *testing.T) {
	var problems []string
	seen := map[string]bool{}
	walkCommands(NewRootCmd(), func(path string, c *cobra.Command) {
		long := strings.TrimSpace(c.Long)
		if long == "" {
			return // presence is the other test's report
		}
		words := wordCount(long)
		if waived, ok := longWordWaiver[path]; ok {
			seen[path] = true
			if words != waived {
				problems = append(problems, fmt.Sprintf("%s: %d words, waiver says %d — update the waiver DOWN when trimming; growing a waived Long is not allowed",
					path, words, waived))
			}
			return
		}
		if words > longWordCeiling {
			problems = append(problems, fmt.Sprintf("%s: %d words; ceiling is %d (state purpose, when to use it, and the one non-obvious constraint — link a `satelle help` topic instead of restating it)",
				path, words, longWordCeiling))
		}
		if n := len(strings.Split(long, "\n")); n > longLineCeiling {
			problems = append(problems, fmt.Sprintf("%s: %d lines; ceiling is %d", path, n, longLineCeiling))
		}
	})
	for path := range longWordWaiver {
		if !seen[path] {
			problems = append(problems, fmt.Sprintf("longWordWaiver has %q, which is not a command with a Long — drop the stale entry", path))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("help text over budget:\n  %s", strings.Join(problems, "\n  "))
	}
}

// wordCount counts whitespace-delimited words.
func wordCount(s string) int {
	n, inWord := 0, false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			n++
			inWord = true
		}
	}
	return n
}

// TestCoverageCatchesANewBareCommand: the guard has to fail for a command added
// TOMORROW, not just describe today's tree — otherwise the surface regresses the
// first time someone adds a verb in a hurry (sty_a499e7f5 AC5).
func TestCoverageCatchesANewBareCommand(t *testing.T) {
	root := &cobra.Command{Use: "satelle"}
	documented := &cobra.Command{Use: "documented", Short: "s", Long: "A Long that says what it is for."}
	documented.AddCommand(&cobra.Command{Use: "bare-child", Short: "s"})
	root.AddCommand(documented)
	root.AddCommand(&cobra.Command{Use: "bare", Short: "s"})
	// Excluded by the enumeration rule, so neither may be reported.
	root.AddCommand(&cobra.Command{Use: "completion", Short: "s"})
	root.AddCommand(&cobra.Command{Use: "secret", Short: "s", Hidden: true})

	got := strings.Join(bareCommands(root), ",")
	if want := "satelle bare,satelle documented bare-child"; got != want {
		t.Errorf("bareCommands = %q, want %q — a nested command, and only the non-exempt ones", got, want)
	}
}
