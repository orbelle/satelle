package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// countGitSpawns puts a `git` shim first on PATH that logs each invocation's
// arguments and then runs the real git, and returns a func reporting the logged
// invocations. It counts spawns without a seam in non-test code.
func countGitSpawns(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "spawns.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// AC5: one hook call loads the process config once. Loading it resolves the
// canonical repo root through `git rev-parse --git-common-dir`, so rule
// evaluation must share the containment check's load rather than make its own:
// the spawn count is one with rules authored, and one with none. SATELLE_CONFIG
// is cleared because, when set, the load never resolves a root and spawns
// nothing, which would make the count vacuous.
//
// That includes a matched command: one that reads the seat or writes the ledger
// opens the store, and the store opens from the same load and resolves its
// runtime dir from the same memoised identity, so it starts no further git. Each
// matched case also checks the ledger row, so the count is not vacuous.
func TestCommitgateLoadsConfigOncePerCall(t *testing.T) {
	const denyEverywhere = "\n[[gate.command_rule]]\npattern = \"orbctl nuke\"\naction = \"deny\"\nmessage = \"never\"\n"
	for _, tc := range []struct {
		name, rules, command string
		seat                 stopcheckSeatState
		rows                 int // command_rule ledger rows the call must write
	}{
		{"no rules", "", "ls", seatMine, 0},
		{"rules, no match", exampleRules(t), "ls", seatMine, 0},
		{"rules, deny match without allow list (no seat read, no row)", denyEverywhere, "orbctl nuke", seatMine, 0},
		{"allowed match, story at an allowed status", exampleRules(t), "orbctl apply -f x", seatMine, 1},
		{"warned match, story engaged", exampleRules(t), "orbctl scale web 2", seatMine, 1},
		{"warned match, no story engaged", exampleRules(t), "orbctl scale web 2", seatNone, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := ruleRepo(t, tc.seat, tc.rules)
			if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			t.Setenv("SATELLE_CONFIG", "")
			spawns := countGitSpawns(t)
			_, _ = commitgateOn(t, "claude", tc.command) // a deny is an error; the count is what is judged
			var loads []string
			for _, s := range spawns() {
				if strings.Contains(s, "--git-common-dir") {
					loads = append(loads, s)
				}
			}
			if len(loads) != 1 {
				t.Errorf("%q started %d `git rev-parse --git-common-dir` (want 1, one config load): %q",
					tc.command, len(loads), loads)
			}
			if got := len(ledgerRows(t, ledger.KindCommandRule)); got != tc.rows {
				t.Errorf("%q wrote %d command_rule ledger rows, want %d", tc.command, got, tc.rows)
			}
		})
	}
}

// A repo that subscribes to a command-policy list reads its cache on the command
// path. Run from a linked worktree, the process root is the main tree's, which
// the call's git-identity memo does not hold, so keying the cache on it would
// start a second `git rev-parse --git-common-dir`. The cache is read at the
// invoking root the memo holds: exactly one spawn, and the list still applies.
func TestCommitgateSubscribedListInLinkedWorktreeStartsOneGitIdentity(t *testing.T) {
	const src = "https://example.com/p.toml"
	repo, _ := ruleRepo(t, seatNone, "\n[[gate.command_policy]]\nname = \"cloud\"\nurl = \""+src+"\"\n")
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "policy-wt", wt, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	seedPolicy(t, repo, "cloud", src, "url",
		"[[command_rule]]\npattern = \"orbctl nuke\"\naction = \"deny\"\nmessage = \"subscribed: never\"\n", time.Now())
	t.Chdir(wt)
	t.Setenv("SATELLE_CONFIG", "")
	spawns := countGitSpawns(t)
	out, err := commitgateOn(t, "claude", "orbctl nuke")
	if err == nil {
		t.Fatalf("the subscribed deny did not apply from the linked worktree:\n%s", out)
	}
	if reason := ruleDenyReason(t, out); !strings.Contains(reason, "subscribed: never") {
		t.Fatalf("reason = %q", reason)
	}
	var loads []string
	for _, s := range spawns() {
		if strings.Contains(s, "--git-common-dir") {
			loads = append(loads, s)
		}
	}
	if len(loads) != 1 {
		t.Errorf("a subscribed call from a linked worktree started %d `git rev-parse --git-common-dir` (want 1): %q", len(loads), loads)
	}
}
