package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gittest"
)

const pkg = "example.com/m/p"

func ev(action, test, output string) string {
	var b strings.Builder
	b.WriteString(`{"Action":"` + action + `","Package":"` + pkg + `"`)
	if test != "" {
		b.WriteString(`,"Test":"` + test + `"`)
	}
	if output != "" {
		b.WriteString(`,"Output":"` + output + `"`)
	}
	b.WriteString("}\n")
	return b.String()
}

// skipStream is one test that skips, with the output go test prints for it.
func skipStream(test string) string {
	return ev("run", test, "") +
		ev("output", test, "=== RUN   "+test+`\n`) +
		ev("output", test, "    x_test.go:9: git not on PATH\\n") +
		ev("output", test, "--- SKIP: "+test+` (0.00s)\n`) +
		ev("skip", test, "")
}

// runWith writes the stream to a fixture, runs skipcheck over a fake child that
// prints it and exits with code, and returns (exit, stdout, stderr).
func runWith(t *testing.T, stream string, code string, allowBody *string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "events.json")
	if err := os.WriteFile(fixture, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if allowBody != nil {
		allow := filepath.Join(dir, "allow.txt")
		if err := os.WriteFile(allow, []byte(*allowBody), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-allow", allow)
	}
	args = append(args, "--", "sh", "-c", `cat "$1"; exit "$2"`, "sh", fixture, code)
	var out, errb bytes.Buffer
	got := run(args, &out, &errb)
	return got, out.String(), errb.String()
}

func ptr(s string) *string { return &s }

func TestFailingChildFailsTheRun(t *testing.T) {
	stream := ev("run", "TestA", "") + ev("fail", "TestA", "") + ev("fail", "", "")
	got, _, _ := runWith(t, stream, "1", ptr(""))
	if got != 1 {
		t.Fatalf("exit = %d, want the child's 1", got)
	}
	// The child's own status is kept, whatever it is.
	if got, _, _ := runWith(t, stream, "3", ptr("")); got != 3 {
		t.Fatalf("exit = %d, want the child's 3", got)
	}
}

func TestUnlistedSkipFailsAndNamesIt(t *testing.T) {
	got, stdout, stderr := runWith(t, skipStream("TestGit"), "0", ptr(""))
	if got == 0 {
		t.Fatal("exit 0, want non-zero for an unlisted skip")
	}
	for _, want := range []string{pkg, "TestGit", "git not on PATH"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("report is missing %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stdout, "--- SKIP: TestGit") {
		t.Errorf("the test output was not echoed:\n%s", stdout)
	}
}

func TestListedSkipPasses(t *testing.T) {
	allow := ptr("# reviewed\n\n" + pkg + " TestGit git is absent on the runner\n")
	if got, _, stderr := runWith(t, skipStream("TestGit"), "0", allow); got != 0 {
		t.Fatalf("exit = %d, want 0 for a listed skip:\n%s", got, stderr)
	}
}

func TestSubtestSkipNeedsItsOwnEntry(t *testing.T) {
	stream := skipStream("TestTable/case_one")
	if got, _, _ := runWith(t, stream, "0", ptr(pkg+" TestTable parent only\n")); got == 0 {
		t.Fatal("a parent entry must not allow a subtest skip")
	}
	if got, _, _ := runWith(t, stream, "0", ptr(pkg+" TestTable/case_one needs a tty\n")); got != 0 {
		t.Fatal("an exact subtest entry must allow the subtest skip")
	}
}

func TestPackageLevelSkipIsIgnored(t *testing.T) {
	stream := ev("output", "", "?   \\texample.com/m/p\\t[no test files]\\n") + ev("skip", "", "")
	if got, _, stderr := runWith(t, stream, "0", ptr("")); got != 0 {
		t.Fatalf("exit = %d, want 0 for a package with no test files:\n%s", got, stderr)
	}
}

func TestRepeatedSkipIsReportedOnce(t *testing.T) {
	stream := skipStream("TestGit") + skipStream("TestGit") + skipStream("TestGit")
	_, _, stderr := runWith(t, stream, "0", ptr(""))
	if n := strings.Count(stderr, " TestGit:"); n != 1 {
		t.Fatalf("TestGit reported %d times, want once:\n%s", n, stderr)
	}
}

func TestPassingRunWithNoSkipsPasses(t *testing.T) {
	stream := ev("run", "TestA", "") + ev("pass", "TestA", "") + ev("pass", "", "")
	if got, _, stderr := runWith(t, stream, "0", ptr("")); got != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", got, stderr)
	}
}

func TestNonJSONLinesPassThrough(t *testing.T) {
	_, stdout, _ := runWith(t, "# example.com/m/p\nx.go:1:1: undefined: y\n", "1", ptr(""))
	if !strings.Contains(stdout, "undefined: y") {
		t.Fatalf("build error was not passed through:\n%s", stdout)
	}
}

func TestMalformedAllowlistFailsBeforeRunningTheChild(t *testing.T) {
	for name, body := range map[string]string{
		"no reason":    pkg + " TestGit\n",
		"one field":    "TestGit\n",
		"duplicate":    pkg + " TestGit a\n" + pkg + " TestGit b\n",
		"second entry": pkg + " TestA fine\n" + pkg + " TestB\n",
	} {
		t.Run(name, func(t *testing.T) {
			// A passing, skip-free stream: only the allowlist can fail this run.
			stream := ev("run", "TestA", "") + ev("pass", "TestA", "")
			got, stdout, stderr := runWith(t, stream, "0", ptr(body))
			if got == 0 {
				t.Fatalf("exit 0, want non-zero for a malformed allowlist (%s)", name)
			}
			if stdout != "" {
				t.Errorf("the child ran despite a bad allowlist:\n%s", stdout)
			}
			if !strings.Contains(stderr, "line ") {
				t.Errorf("error does not name the line:\n%s", stderr)
			}
		})
	}
}

func TestMissingAllowlistFileFails(t *testing.T) {
	var out, errb bytes.Buffer
	args := []string{"-allow", filepath.Join(t.TempDir(), "absent.txt"), "--", "true"}
	if got := run(args, &out, &errb); got == 0 {
		t.Fatal("exit 0, want non-zero for a missing allowlist file")
	}
}

func TestNoCommandIsAUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if got := run([]string{"-allow", ""}, &out, &errb); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

func TestParseAllowlistKeepsTheReason(t *testing.T) {
	allow, err := ParseAllowlist(strings.NewReader("# c\n" + pkg + " TestX/sub needs the real  network\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := allow[key{pkg, "TestX/sub"}]; got != "needs the real network" {
		t.Fatalf("reason = %q", got)
	}
}

// TestStallLivenessRules drives the running-set rules with a fake clock, so no
// case depends on real elapsed time.
func TestStallLivenessRules(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	step := func(w *Watch, s int, action, test string) {
		w.observe(event{Action: action, Package: pkg, Test: test}, at(s))
	}
	names := func(w *Watch, s int) string {
		var n []string
		for _, h := range w.expired(at(s)) {
			n = append(n, h.Test)
		}
		return strings.Join(n, ",")
	}

	t.Run("a started test with no result is stalled", func(t *testing.T) {
		w := newWatch(10 * time.Second)
		step(w, 0, "run", "TestHang")
		if got := names(w, 10); got != "" {
			t.Fatalf("at the deadline = %q, want none (strictly past it)", got)
		}
		if got := names(w, 11); got != "TestHang" {
			t.Fatalf("past the deadline = %q, want TestHang", got)
		}
	})
	t.Run("a finished test is not stalled", func(t *testing.T) {
		for _, end := range []string{"pass", "fail", "skip"} {
			w := newWatch(10 * time.Second)
			step(w, 0, "run", "TestA")
			step(w, 1, end, "TestA")
			if got := names(w, 100); got != "" {
				t.Fatalf("after %s = %q, want none", end, got)
			}
		}
	})
	t.Run("a paused test is not stalled, and counts again from cont", func(t *testing.T) {
		w := newWatch(10 * time.Second)
		step(w, 0, "run", "TestPar")
		step(w, 1, "pause", "TestPar")
		if got := names(w, 100); got != "" {
			t.Fatalf("while paused = %q, want none", got)
		}
		step(w, 100, "cont", "TestPar")
		if got := names(w, 105); got != "" {
			t.Fatalf("5s after cont = %q, want none", got)
		}
		if got := names(w, 111); got != "TestPar" {
			t.Fatalf("11s after cont = %q, want TestPar", got)
		}
	})
	t.Run("a repeated run under -count restarts the clock", func(t *testing.T) {
		w := newWatch(10 * time.Second)
		step(w, 0, "run", "TestA")
		step(w, 5, "pass", "TestA")
		step(w, 50, "run", "TestA")
		if got := names(w, 55); got != "" {
			t.Fatalf("5s into the second run = %q, want none", got)
		}
	})
	t.Run("subtest progress keeps its running parent alive", func(t *testing.T) {
		w := newWatch(10 * time.Second)
		step(w, 0, "run", "TestP")
		step(w, 8, "run", "TestP/a")
		step(w, 9, "pass", "TestP/a")
		step(w, 16, "run", "TestP/b")
		if got := names(w, 24); got != "" {
			t.Fatalf("parent with a live subtest = %q, want none", got)
		}
		// The hung subtest is named, with the parent that is waiting on it.
		if got := names(w, 27); got != "TestP,TestP/b" {
			t.Fatalf("hung subtest = %q, want TestP,TestP/b", got)
		}
	})
	t.Run("a package result closes the tests it left open", func(t *testing.T) {
		w := newWatch(10 * time.Second)
		step(w, 0, "run", "TestCrashed")
		w.observe(event{Action: "fail", Package: pkg}, at(1))
		if got := names(w, 100); got != "" {
			t.Fatalf("after the package failed = %q, want none", got)
		}
	})
	t.Run("a nil watch ignores everything", func(t *testing.T) {
		var w *Watch
		w.observe(event{Action: "run", Package: pkg, Test: "TestA"}, at(0))
	})
}

// TestStallOffByDefault: without -stall a slow test is left alone.
func TestStallOffByDefault(t *testing.T) {
	var out, errb bytes.Buffer
	fixture := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(fixture, []byte(ev("run", "TestSlow", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--", "sh", "-c", `cat "$1"; sleep 0.3`, "sh", fixture}
	if got := run(args, &out, &errb); got != 0 {
		t.Fatalf("exit = %d, want 0 with no -stall:\n%s", got, errb.String())
	}
	if strings.Contains(errb.String(), "stalled") {
		t.Fatalf("a stall was reported with no -stall:\n%s", errb.String())
	}
}

func TestNegativeStallIsAUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if got := run([]string{"-stall", "-1s", "--", "true"}, &out, &errb); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

// TestScratchProbe is an inert-by-default hook for the negative proofs of
// sty_53c3f311 (a skip that is not allowed; a failure the checker must carry).
// It does nothing unless SATELLE_TEST_PROBE_SKIPCHECK is set (hermetic.sh
// forwards SATELLE_TEST_PROBE_*), so it is inert by default.
func TestScratchProbe(t *testing.T) {
	switch os.Getenv("SATELLE_TEST_PROBE_SKIPCHECK") {
	case "skip":
		t.Skip("scratch skip, not on the allowlist")
	case "fail":
		t.Fatal("scratch failure")
	}
}

// treeRepo is a git repo with one committed file, for the -tree guard.
func treeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "kept.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "kept.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "add")
	return dir
}

// runTree runs skipcheck over a child shell script, with the guard on tree when
// tree is non-empty, and returns (exit, stderr). The script sees tree as $1.
func runTree(t *testing.T, tree, script string) (int, string) {
	t.Helper()
	var args []string
	if tree != "" {
		args = append(args, "-tree", tree)
	}
	args = append(args, "--", "sh", "-c", script, "sh", tree)
	var out, errb bytes.Buffer
	return run(args, &out, &errb), errb.String()
}

func TestTreeGuardFailsWhenTheRunWritesIntoTheTree(t *testing.T) {
	for name, c := range map[string]struct{ script, want string }{
		"a new file":     {`printf 'package p\n' > "$1/planted.go"`, "appeared planted.go"},
		"a changed file": {`printf 'package q\n' > "$1/kept.go"`, "changed kept.go"},
		"a removed file": {`rm "$1/kept.go"`, "changed kept.go"},
	} {
		t.Run(name, func(t *testing.T) {
			got, stderr := runTree(t, treeRepo(t), c.script)
			if got != 1 {
				t.Fatalf("exit = %d, want 1 for a run that changed the tree:\n%s", got, stderr)
			}
			if !strings.Contains(stderr, "changed the source tree") || !strings.Contains(stderr, c.want) {
				t.Errorf("report is missing %q:\n%s", c.want, stderr)
			}
		})
	}
}

func TestTreeGuardPassesWhenTheRunLeavesTheTreeAlone(t *testing.T) {
	for name, script := range map[string]string{
		"writes only elsewhere":        `printf x > "$1/../skipcheck-scratch-$$"; rm "$1/../skipcheck-scratch-$$"`,
		"plants a file and removes it": `printf x > "$1/gone.go"; rm "$1/gone.go"`,
		"does nothing to the tree":     `true`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := treeRepo(t)
			// Already dirty before the run and left alone by it: not the run's doing.
			if err := os.WriteFile(filepath.Join(dir, "preexisting.go"), []byte("package p\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, stderr := runTree(t, dir, script); got != 0 {
				t.Fatalf("exit = %d, want 0:\n%s", got, stderr)
			}
		})
	}
}

func TestTreeGuardNamesAnUntrackedFileTheRunRemoved(t *testing.T) {
	dir := treeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "mine.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, stderr := runTree(t, dir, `rm "$1/mine.go"`)
	if got != 1 || !strings.Contains(stderr, "vanished mine.go") {
		t.Fatalf("exit = %d, want 1 naming `vanished mine.go`:\n%s", got, stderr)
	}
}

func TestTreeGuardOffByDefault(t *testing.T) {
	dir := treeRepo(t)
	if got, stderr := runTree(t, "", `printf x > "`+filepath.Join(dir, "planted.go")+`"`); got != 0 {
		t.Fatalf("exit = %d, want 0 without -tree:\n%s", got, stderr)
	}
}

func TestTreeGuardOnANonRepoIsASetupError(t *testing.T) {
	if got, _ := runTree(t, t.TempDir(), `true`); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}
