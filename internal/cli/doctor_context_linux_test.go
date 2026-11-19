//go:build linux

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func contextReport(t *testing.T) (issues []doctorIssue, notes []string, err error) {
	t.Helper()
	out, err := run(t, "", "doctor", "--json", "--session", "s-hook")
	var rep doctorReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("doctor --json: %v\n%s", jerr, out)
	}
	for _, is := range rep.Issues {
		if is.Check == checkContextName {
			issues = append(issues, is)
		}
	}
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, checkContextName+": ") {
			notes = append(notes, n)
		}
	}
	return issues, notes, err
}

func padInject(t *testing.T, f *doctorFixture) {
	t.Helper()
	writeFile(t, filepath.Join(f.dir, "plain.md"), "# Plain title\n\n"+indexNonceB+"\n\n"+strings.Repeat("filler line for an inline form larger than the index\n", 80))
}

func indexedNote(harness string, inline, limit int) string {
	return fmt.Sprintf("context: %s: the session-start context is %d bytes with every [hook] inject file inline, over its limit of %d bytes (.satelle/harness.toml [%s] context_limit_bytes); a session receives the files as an index", harness, inline, limit, harness)
}

func TestDoctorNotesAnIndexedHarness(t *testing.T) {
	f := indexFixture(t)
	padInject(t, f)
	_, inline := f.context(t, hook.Claude)
	limit := len(inline) - 1
	f.limit(t, hook.Claude, limit)
	_, raw := f.context(t, hook.Claude)
	if len(raw) > limit {
		t.Fatalf("the fixture's index form is %d bytes, over the limit %d", len(raw), limit)
	}
	for _, l := range []int{limit, len(raw)} {
		f.limit(t, hook.Claude, l)
		issues, notes, err := contextReport(t)
		want := indexedNote(hook.Claude, len(inline), l)
		if len(issues) != 0 || len(notes) != 1 || notes[0] != want {
			t.Fatalf("limit %d: want no context issue and the note %q, got %+v and %q (err %v)", l, want, issues, notes, err)
		}
		if !strings.Contains(notes[0], "a session receives the files as an index") {
			t.Errorf("limit %d: the note does not say a session receives an index: %q", l, notes[0])
		}
		if text, _ := run(t, "", "doctor", "--session", "s-hook"); !strings.Contains(text, "note: "+want+"\n") {
			t.Errorf("limit %d: plain doctor does not print the note:\n%s", l, text)
		}
	}
}

func TestDoctorReportsContextOverTheLimit(t *testing.T) {
	f := indexFixture(t)
	padInject(t, f)
	_, inline := f.context(t, hook.Claude)
	f.limit(t, hook.Claude, len(inline)-1)
	_, raw := f.context(t, hook.Claude)
	f.limit(t, hook.Claude, len(raw)-1)
	issues, notes, err := contextReport(t)
	if len(issues) != 1 || len(notes) != 0 {
		t.Fatalf("want one context issue and no context note, got %+v and %q", issues, notes)
	}
	if ExitCodeOf(err) != ExitError || err == nil {
		t.Errorf("doctor with a context issue: %v, want exit %d", err, ExitError)
	}
	is := issues[0]
	for _, want := range []string{
		"claude: the session-start context is " + strconv.Itoa(len(raw)) + " bytes as an index of the [hook] inject files",
		"over its limit of " + strconv.Itoa(len(raw)-1) + " bytes (.satelle/harness.toml [claude] context_limit_bytes)",
	} {
		if !strings.Contains(is.Problem, want) {
			t.Errorf("the problem lacks %q:\n%s", want, is.Problem)
		}
	}
	if !strings.Contains(is.Fix, kernel.HarnessFile) || !strings.Contains(is.Fix, kernel.SessionFile) || !strings.Contains(is.Fix, "[hook] inject") || is.Resolve != "" {
		t.Errorf("fix %q, resolve %q", is.Fix, is.Resolve)
	}
	out, _ := run(t, "", "doctor", "--resolve", "--json", "--session", "s-hook")
	var res resolveReport
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("doctor --resolve --json: %v\n%s", err, out)
	}
	left := false
	for _, l := range res.Left {
		if l.Check == checkContextName && l.Why == leftNoFix {
			left = true
		}
	}
	if !left {
		t.Errorf("doctor --resolve does not leave the context issue as unprovable: %+v", res.Left)
	}
}

func TestDoctorReportsNoContextIssueWhenItFits(t *testing.T) {
	f := indexFixture(t)
	_, inline := f.context(t, hook.Claude)
	for _, over := range []int{0, 1000} {
		f.limit(t, hook.Claude, len(inline)+over)
		if issues, notes, _ := contextReport(t); len(issues) != 0 || len(notes) != 0 {
			t.Errorf("a limit %d over the inline size: issues %+v, notes %q", over, issues, notes)
		}
	}
	writeHookSettings(t, f.dir, "inject = []\n")
	f.limit(t, hook.Claude, 1)
	if issues, notes, _ := contextReport(t); len(issues) != 0 || len(notes) != 0 {
		t.Errorf("no inject file at limit 1: issues %+v, notes %q", issues, notes)
	}
}

func TestDoctorNotesAnUnbudgetedHarness(t *testing.T) {
	f := indexFixture(t)
	unbudgeted := regexp.MustCompile(`^context: claude: session-start context is [0-9]+ bytes, unbudgeted: \.satelle/harness\.toml has no \[claude\] context_limit_bytes$`)
	check := func(what string) {
		t.Helper()
		issues, notes, _ := contextReport(t)
		if len(issues) != 0 || len(notes) != 1 || !unbudgeted.MatchString(notes[0]) {
			t.Fatalf("%s: want the one unbudgeted note for claude, got issues %+v and notes %q", what, issues, notes)
		}
		if text, _ := run(t, "", "doctor", "--session", "s-hook"); !strings.Contains(text, "note: "+notes[0]+"\n") {
			t.Errorf("%s: plain doctor does not print the note:\n%s", what, text)
		}
	}
	check("no harness.toml")
	f.limit(t, hook.Pi, 1)
	check("a limit for a harness not in use")
}

func TestDoctorUnbudgetedCountIsWhatTheHookWrote(t *testing.T) {
	for _, h := range []string{hook.Claude, hook.Cursor, hook.Grok} {
		t.Run(h, func(t *testing.T) {
			f := indexFixture(t)
			must(t, "agents", "install", h)
			_, raw := f.context(t, h)
			_, notes, _ := contextReport(t)
			prefix := fmt.Sprintf("context: %s: session-start context is ", h)
			got := -1
			for _, n := range notes {
				if rest, ok := strings.CutPrefix(n, prefix); ok {
					num, _, _ := strings.Cut(rest, " ")
					got, _ = strconv.Atoi(num)
				}
			}
			if got != len(raw) {
				t.Fatalf("doctor's unbudgeted count for %s is %d, and satelle hook context wrote %d bytes; notes %q\n%s", h, got, len(raw), notes, raw)
			}
			if _, again := f.context(t, h); again != raw {
				t.Errorf("the hook's output changed after doctor ran:\n%s\n--\n%s", raw, again)
			}
		})
	}
}

func TestDoctorContextCheckIsNotInTheHooksOwnBlock(t *testing.T) {
	f := indexFixture(t)
	_, inline := f.context(t, hook.Claude)
	f.limit(t, hook.Claude, len(inline)-1)
	ctx, raw := f.context(t, hook.Claude)
	hasAll(t, "index form", ctx, indexReadLine, "check:fixture-check (1): one")
	hasNone(t, "the hook's own doctor block", raw, "the session-start context is", checkContextName+" (1)")
	if err := os.Remove(filepath.Join(f.dir, "FAIL")); err != nil {
		t.Fatal(err)
	}
	if ctx, _ := f.context(t, hook.Claude); strings.Contains(ctx, "satelle doctor:") {
		t.Errorf("with the context issue as doctor's only finding, the hook printed a doctor block:\n%s", ctx)
	}
}
