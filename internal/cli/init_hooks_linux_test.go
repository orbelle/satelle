//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hook"
)

func wiringStates(t *testing.T, dir string) map[string]string {
	t.Helper()
	states := map[string]string{}
	for _, w := range hook.WiringOf(dir) {
		states[w.Harness] = w.State
	}
	return states
}

func wantOnlyWired(t *testing.T, dir string, wired ...string) {
	t.Helper()
	for h, state := range wiringStates(t, dir) {
		want := hook.WiringNotInstalled
		if slices.Contains(wired, h) {
			want = hook.WiringInstalled
		}
		if state != want {
			t.Errorf("%s: %s is %q, want %q", dir, h, state, want)
		}
	}
}

func TestInitHooksTakesTheHarnessAsASeparateWord(t *testing.T) {
	const cmdLine = "  satelle agents install <harness>   # gives that harness's sessions satelle's context and the edit gate; commit the file it writes\n"
	t.Run("--hooks claude installs what --hooks=claude does", func(t *testing.T) {
		spaced := isolate(t)
		spacedOut := must(t, "init", "--hooks", "claude")
		valued := isolate(t)
		valuedOut := must(t, "init", "--hooks=claude")
		if a, b := strings.ReplaceAll(spacedOut, spaced, "<dir>"), strings.ReplaceAll(valuedOut, valued, "<dir>"); a != b {
			t.Fatalf("outputs differ:\n--hooks claude:\n%s\n--hooks=claude:\n%s", a, b)
		}
		a, err := os.ReadFile(filepath.Join(spaced, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(valued, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf(".claude/settings.json differs:\n%s\n---\n%s", a, b)
		}
		wantOnlyWired(t, spaced, hook.Claude)
		wantOnlyWired(t, valued, hook.Claude)
	})
	t.Run("--hooks claude,pi installs both", func(t *testing.T) {
		dir := isolate(t)
		out := must(t, "init", "--hooks", "claude,pi")
		for _, rel := range []string{".claude/settings.json", ".pi/extensions/satelle.ts"} {
			if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
				t.Errorf("%s: %v", rel, err)
			}
		}
		for _, rel := range []string{".grok/hooks/satelle.json", ".cursor/hooks.json"} {
			if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
				t.Errorf("%s exists (err %v)", rel, err)
			}
		}
		if !strings.HasSuffix(out, "harness hooks not installed: agy, grok, cursor\n"+cmdLine) {
			t.Fatalf("init --hooks claude,pi:\n%s", out)
		}
		wantOnlyWired(t, dir, hook.Claude, hook.Pi)
	})
	t.Run("bare --hooks installs claude", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init", "--hooks")
		wantOnlyWired(t, dir, hook.Claude)
	})
	t.Run("bare --hooks before another flag installs claude", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init", "--hooks", "--session", "s")
		wantOnlyWired(t, dir, hook.Claude)
	})
}

func TestInitHarnessesResolvesTheFlagAndItsWord(t *testing.T) {
	bare := []string{hooksBare}
	cases := []struct {
		name    string
		flag    []string
		changed bool
		args    []string
		want    []string
		refused bool
	}{
		{name: "not given", want: []string{}},
		{name: "bare", flag: bare, changed: true, want: []string{hook.Claude}},
		{name: "=claude", flag: []string{hook.Claude}, changed: true, want: []string{hook.Claude}},
		{name: "=claude,pi", flag: []string{hook.Claude, hook.Pi}, changed: true, want: []string{hook.Claude, hook.Pi}},
		{name: "bare claude", flag: bare, changed: true, args: []string{"claude"}, want: []string{hook.Claude}},
		{name: "bare claude,pi", flag: bare, changed: true, args: []string{"claude,pi"}, want: []string{hook.Claude, hook.Pi}},
		{name: "=pi claude", flag: []string{hook.Pi}, changed: true, args: []string{"claude"}, refused: true},
		{name: "bare claude pi", flag: bare, changed: true, args: []string{"claude", "pi"}, refused: true},
		{name: "bare twice claude", flag: []string{hooksBare, hooksBare}, changed: true, args: []string{"claude"}, refused: true},
		{name: "bare and =pi claude", flag: []string{hooksBare, hook.Pi}, changed: true, args: []string{"claude"}, refused: true},
	}
	for _, c := range cases {
		got, err := initHarnesses(c.flag, c.changed, c.args)
		if c.refused {
			if err == nil {
				t.Errorf("%s: resolved %q, want refused", c.name, got)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if slices.Contains(hook.Harnesses, hooksBare) {
		t.Errorf("the bare sentinel %q is a harness name", hooksBare)
	}
}

func TestInitRefusesAStrayWordBeforeWriting(t *testing.T) {
	root := NewRootCmd()
	initC, _, err := root.Find([]string{"init"})
	if err != nil {
		t.Fatal(err)
	}
	noArgs := cobra.NoArgs(initC, []string{"claude"}).Error()
	for _, args := range [][]string{
		{"init", "claude"},
		{"init", "--hooks=pi", "claude"},
		{"init", "--hooks", "claude", "pi"},
	} {
		dir := isolate(t)
		out, err := run(t, "", args...)
		if err == nil {
			t.Errorf("satelle %s succeeded:\n%s", strings.Join(args, " "), out)
			continue
		}
		if args[1] == "claude" && err.Error() != noArgs {
			t.Errorf("satelle init claude: %q, want cobra's %q", err, noArgs)
		}
		for _, rel := range []string{".satelle", ".claude", ".pi", ".grok", ".cursor"} {
			if _, statErr := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(statErr) {
				t.Errorf("satelle %s left %s (err %v)", strings.Join(args, " "), rel, statErr)
			}
		}
	}
}
