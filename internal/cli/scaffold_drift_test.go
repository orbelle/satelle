package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
)

func TestDetectScaffoldDrift_CleanAfterWrite(t *testing.T) {
	repo := t.TempDir()
	writeCanonicalHooks(t, repo)
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("canonical deploy must be clean: %v", fs)
	}
}

// writeCanonicalHooks deploys the hook scripts and both harnesses' canonical
// settings, so the repo carries no hook drift.
func writeCanonicalHooks(t *testing.T, repo string) {
	t.Helper()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	// Materialise canonical settings for both harnesses.
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), buildClaudeHookSettings(repo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".grok", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(grokHooksRel)), buildGrokHookSettings(repo), 0o644); err != nil {
		t.Fatal(err)
	}
}

// AC9 (sty_b336c2b0): with current hooks, a removed [sync] scope key in either
// config file is a finding naming the file, the key and the heal; once removed
// there is none.
func TestDetectScaffoldDriftReportsSyncScopeKeys(t *testing.T) {
	repo := t.TempDir()
	writeCanonicalHooks(t, repo)
	data := filepath.Join(repo, config.DefaultDataDir)
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(data, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(config.ConfigName, "[sync]\nall = \"personal\"\nproject = \"p\"\n[substrate_roots]\nskills = \".\"\n")
	write(config.LocalConfigName, "[sync]\ndocuments = \"local\"\n")
	fs := DetectScaffoldDrift(repo)
	want := map[string]string{
		config.DefaultDataDir + "/" + config.ConfigName:      "[sync] all is removed — run satelle init",
		config.DefaultDataDir + "/" + config.LocalConfigName: "[sync] documents is removed — run satelle init",
	}
	if len(fs) != len(want) {
		t.Fatalf("findings = %v, want one per removed key", fs)
	}
	for _, f := range fs {
		if f.Kind != "config" || want[f.Path] != f.Detail {
			t.Errorf("finding %+v, want config %q for %s", f, want[f.Path], f.Path)
		}
	}

	write(config.ConfigName, "[sync]\nproject = \"p\"\n")
	write(config.LocalConfigName, "")
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Errorf("no scope key left, yet findings: %v", fs)
	}
}

// TestDetectScaffoldDrift_GrokMissingHarnessFlag pins AC7 (sty_719c4a7b):
// doctor reports a deployed grok hooks file whose SessionStart,
// UserPromptSubmit or Stop command omits --harness grok, even though the
// freshly built scaffold (asserted clean above) never does.
func TestDetectScaffoldDrift_GrokMissingHarnessFlag(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".grok", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "satelle reindex" }, { "type": "command", "command": "satelle hook context" } ] }
    ],
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook prompt" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook stopcheck" } ] }
    ]
  }
}
`
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(grokHooksRel)), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := DetectScaffoldDrift(repo)
	var events []string
	for _, f := range fs {
		if f.Path == grokHooksRel {
			events = append(events, f.Detail)
		}
	}
	for _, want := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		found := false
		for _, d := range events {
			if strings.Contains(d, want) && strings.Contains(d, "--harness grok") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a --harness grok finding naming %s, got: %v", want, events)
		}
	}
}

func TestDetectScaffoldDrift_LegacyInlineCommand(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Previous-generation inline wrapper (sty_c75c73ed) — the vire incident shape.
	legacy := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          { "type": "command", "command": "sh -c '#satelle-failvisible\nfor c in \"$HOME/.local/bin/satelle\"; do :; done; satelle hook gate'" }
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := DetectScaffoldDrift(repo)
	if len(fs) == 0 {
		t.Fatal("legacy inline gate must report drift")
	}
	joined := ""
	for _, f := range fs {
		joined += f.Kind + " " + f.Path + " " + f.Detail + "\n"
	}
	if !strings.Contains(joined, "command") {
		t.Fatalf("want command drift, got:\n%s", joined)
	}
	if !strings.Contains(joined, "missing") {
		t.Fatalf("want missing script, got:\n%s", joined)
	}
	warn := formatScaffoldDriftWarning(fs)
	if !strings.Contains(warn, "satelle init") {
		t.Fatalf("warning must name heal command: %s", warn)
	}
}

func TestDetectScaffoldDrift_StaleScriptContent(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	rel := hookScriptRel("claude", "gate")
	path := filepath.Join(repo, filepath.FromSlash(rel))
	// The faulty generation that mixed structured stdout with exit 2 while
	// discarding the blocking reason on stderr must be recognised as drift.
	stale := `#!/bin/sh
o=$(satelle hook gate --harness claude 2>/dev/null); code=$?
[ -n "$o" ] && printf '%s\n' "$o"
[ "$code" -eq 0 ] && exit 0
exit 2
`
	if err := os.WriteFile(path, []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	// Settings still point at the script form so harness is "deployed".
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), buildClaudeHookSettings(repo), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := DetectScaffoldDrift(repo)
	if len(fs) == 0 {
		t.Fatal("stale script content must drift")
	}
	found := false
	for _, f := range fs {
		if f.Path == rel && f.Kind == "content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want content finding for %s: %v", rel, fs)
	}
}

func TestDetectScaffoldDrift_NoHarnessSkipSafe(t *testing.T) {
	repo := t.TempDir()
	// Empty repo — no settings, no scripts.
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("empty repo must be clean: %v", fs)
	}
}

func TestDetectScaffoldDrift_HealClears(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"satelle hook gate || exit 2"}]}]}}`
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(DetectScaffoldDrift(repo)) == 0 {
		t.Fatal("pre-heal must drift")
	}
	if _, _, _, err := ensureClaudeHooks(repo); err != nil {
		t.Fatal(err)
	}
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("post-heal must be clean: %v", fs)
	}
}

// --- sty_e56ea643: stale scaffolding warns, never refuses ---------------------

// driftedRepo returns a hermetic initialised repo whose harness scaffolding is
// stale in the two ways a release changes it: the wrapper script differs from
// the binary's canonical bytes, and a PreToolUse command is not the canonical
// script form. The build is stamped as a release (drift only fires off a dev
// build) and the repo's deployed.version stamp is set to stamp.
func driftedRepo(t *testing.T, stamp string) string {
	t.Helper()
	repo := tempRepo(t)
	old := buildinfo.Version
	buildinfo.Version = "0.0.900"
	t.Cleanup(func() { buildinfo.Version = old })
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(satelleHookScriptRel)), []byte("#!/bin/sh\n# an older wrapper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"satelle hook gate || exit 2"}]}]}}`
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, config.DefaultDataDir, deployedVersionName), []byte("satelle.version: "+stamp+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	return repo
}

// runRootSplit runs one command with stdout and stderr captured apart, so a
// warning on stderr can be told from the JSON a store verb prints on stdout.
func runRootSplit(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	resetFlagState(root)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = executeRoot(root)
	return out.String(), errBuf.String(), err
}

// TestOrdinaryCommandsWarnOnStaleScaffolding pins AC1 and AC2 side by side: the
// same stale scaffolding warns and runs when the release declares nothing
// breaking, and only the CHANGELOG ### Breaking gate (a stamp behind a Breaking
// release) still refuses.
func TestOrdinaryCommandsWarnOnStaleScaffolding(t *testing.T) {
	t.Run("warns and runs when nothing breaking", func(t *testing.T) {
		driftedRepo(t, "0.0.900")
		stdout, stderr, err := runRootSplit(t, "", "story", "create", "--title", "drifted", "--tags", "a")
		if err != nil {
			t.Fatalf("story create must run against stale scaffolding: %v\n%s", err, stderr)
		}
		id := storyIDFromJSON(t, stdout)
		for _, args := range [][]string{
			{"story", "list"},
			{"story", "get", id},
			{"story", "set", id, "--priority", "high"},
			{"ledger", "list", "--story", id},
			{"sync"},
		} {
			stdout, stderr, err := runRootSplit(t, "", args...)
			if err != nil {
				t.Errorf("%v must run against stale scaffolding: %v\n%s", args, err, stderr)
				continue
			}
			var warns []string
			for _, ln := range strings.Split(stderr, "\n") {
				if strings.Contains(ln, "scaffolding is stale") {
					warns = append(warns, ln)
				}
			}
			if len(warns) != 1 {
				t.Errorf("%v: want exactly one warning line on stderr, got %d:\n%s", args, len(warns), stderr)
				continue
			}
			for _, want := range []string{satelleHookScriptRel, ".claude/settings.json", "satelle init", "satelle-managed", "idempotent"} {
				if !strings.Contains(warns[0], want) {
					t.Errorf("%v: warning missing %q: %s", args, want, warns[0])
				}
			}
			if strings.Contains(stdout, "scaffolding is stale") {
				t.Errorf("%v: the warning must stay off stdout:\n%s", args, stdout)
			}
		}
	})

	t.Run("hooks never see the warning or refuse", func(t *testing.T) {
		driftedRepo(t, "0.0.900")
		stdout, stderr, err := runRootSplit(t, `{"session_id":"s"}`, "hook", "prompt")
		if err != nil {
			t.Fatalf("hook prompt must not fail on stale scaffolding: %v", err)
		}
		if strings.Contains(stderr, "scaffolding is stale") {
			t.Errorf("hook stderr must not carry the ordinary-command warning:\n%s", stderr)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(stdout), &v); err != nil {
			t.Errorf("hook stdout must stay valid JSON: %v\n%s", err, stdout)
		}
	})

	// 0.0.830 is the newest shipped Breaking release that declares
	// `init-heals:` (`satelle init` removes the budget keys), so a stamp
	// behind it is refused with init as the heal and the refusal names it. The warn-once path for manual
	// migrations is covered on synthetic entries in breaking_warn_test.go and
	// drift_test.go; the shipped corpus no longer reaches it from an old stamp.
	t.Run("refuses behind an init-heals Breaking release — init is the heal", func(t *testing.T) {
		driftedRepo(t, "0.0.100") // the shipped 0.0.830 Breaking entry is in range
		stdout, _, err := runRootSplit(t, "", "story", "create", "--title", "behind", "--tags", "a")
		if err == nil {
			t.Fatalf("story create must refuse behind an init-heals Breaking release:\n%s", stdout)
		}
		for _, want := range []string{"BREAKING release 0.0.830", "init-heals:", "Run `satelle init` to heal."} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal missing %q: %v", want, err)
			}
		}
	})

	t.Run("hooks run behind a manual-migration Breaking release", func(t *testing.T) {
		driftedRepo(t, "0.0.100")
		if _, _, err := runRootSplit(t, `{"session_id":"s"}`, "hook", "prompt"); err != nil {
			t.Fatalf("hook prompt must not fail behind a manual-migration Breaking release: %v", err)
		}
	})

	t.Run("still refuses an unstamped repo — init is the heal", func(t *testing.T) {
		repo := driftedRepo(t, "0.0.100")
		if err := os.Remove(filepath.Join(repo, config.DefaultDataDir, deployedVersionName)); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := runRootSplit(t, "", "story", "list")
		if err == nil {
			t.Fatal("an unstamped repo must still refuse — init establishes the baseline")
		}
		if !strings.Contains(err.Error(), "satelle init") {
			t.Errorf("refusal must name the heal: %v", err)
		}
		if strings.Contains(stderr, "scaffolding is stale") {
			t.Errorf("a refused command must not also print the drift warning:\n%s", stderr)
		}
	})
}

func storyIDFromJSON(t *testing.T, s string) string {
	t.Helper()
	var v struct {
		ID    string `json:"id"`
		Story struct {
			ID string `json:"id"`
		} `json:"story"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("story create output is not JSON: %v\n%s", err, s)
	}
	if v.ID != "" {
		return v.ID
	}
	if v.Story.ID == "" {
		t.Fatalf("no story id in output:\n%s", s)
	}
	return v.Story.ID
}

func TestWarnScaffoldDriftSilentCases(t *testing.T) {
	// Dev build: the drift is real but the build never self-gates.
	repo := tempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"satelle hook gate || exit 2"}]}]}}`
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	warnScaffoldDrift(repo, &buf)
	if buf.Len() != 0 {
		t.Errorf("a dev build must stay silent, got %q", buf.String())
	}
	// Release build, clean repo and uninitialised repo: silent.
	old := buildinfo.Version
	buildinfo.Version = "0.0.900"
	t.Cleanup(func() { buildinfo.Version = old })
	warnScaffoldDrift(t.TempDir(), &buf)
	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	warnScaffoldDrift(clean, &buf)
	if buf.Len() != 0 {
		t.Errorf("uninitialised or clean repos must stay silent, got %q", buf.String())
	}
}

func TestFormatScaffoldDriftOneLine(t *testing.T) {
	if got := formatScaffoldDriftOneLine(nil); got != "" {
		t.Fatalf("no findings must be silent, got %q", got)
	}
	got := formatScaffoldDriftOneLine([]ScaffoldFinding{
		{Path: "a", Kind: "content", Detail: "x"},
		{Path: "a", Kind: "content", Detail: "y"},
		{Path: "b", Kind: "command", Detail: "z"},
	})
	if strings.Contains(got, "\n") || !strings.Contains(got, "(2)") || strings.Count(got, "a[content]") != 1 || !strings.Contains(got, "b[command]") {
		t.Errorf("one line, each artifact once: %q", got)
	}
}

// TestInitHealsStaleScaffoldingIdempotently pins AC3: every stale item is a
// satelle-owned managed file, init rewrites it, and a second init changes nothing.
func TestInitHealsStaleScaffoldingIdempotently(t *testing.T) {
	repo := driftedRepo(t, "0.0.900")
	if len(DetectScaffoldDrift(repo)) == 0 {
		t.Fatal("fixture must drift before heal")
	}
	heal := func() {
		t.Helper()
		if err := runInitTest(t, io.Discard, repo); err != nil {
			t.Fatalf("init: %v", err)
		}
	}
	managed := []string{satelleHookScriptRel, ".claude/settings.json"}
	read := func() map[string]string {
		out := map[string]string{}
		for _, rel := range managed {
			b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			out[rel] = string(b)
		}
		return out
	}
	heal()
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("init must heal every stale item: %v", fs)
	}
	first := read()
	heal()
	if fs := DetectScaffoldDrift(repo); len(fs) != 0 {
		t.Fatalf("second init must leave no findings: %v", fs)
	}
	second := read()
	for _, rel := range managed {
		if first[rel] != second[rel] {
			t.Errorf("init is not idempotent for %s", rel)
		}
	}
}

// TestDoctorStillReportsScaffoldStale pins AC4: the warning is a courtesy on
// ordinary commands; doctor still records the drift as a scaffold.stale finding.
func TestDoctorStillReportsScaffoldStale(t *testing.T) {
	repo := driftedRepo(t, "0.0.900")
	found := false
	for _, f := range scaffoldFindings(repo) {
		if f.ID == health.IDScaffoldStale {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor must still report %s on a drifted repo", health.IDScaffoldStale)
	}
}
