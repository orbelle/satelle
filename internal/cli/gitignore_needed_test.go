package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitignoreHasSetting (sty_dbc0be85 AC2): a setting is a positive or
// negated rule targeting the path or a path under it.
func TestGitignoreHasSetting(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"exact", ".satelle", true},
		{"leading slash", "/.satelle", true},
		{"trailing slash", ".satelle/", true},
		{"slash star star", ".satelle/**", true},
		{"slash star", ".satelle/*", true},
		{"negation under it", "!.satelle/x", true},
		{"sub-path only", ".satelle/logs", true},
		{"comment", "# .satelle/", false},
		{"unrelated", "node_modules/", false},
		{"prefix is not a setting", ".satellex/", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitignoreHasSetting(tc.text, ".satelle"); got != tc.want {
				t.Errorf("gitignoreHasSetting(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestGitignoreNeededSatelle (sty_45bf867b AC1): the needed set is the
// tracked-substrate shape — never .satelle/ itself — each line added only when
// no rule already targets its path, tracked or not.
func TestGitignoreNeededSatelle(t *testing.T) {
	globs := strings.Join(managedEditExemptGlobs, ",")
	tracked := strings.Join(satelleTrackedSubstrateIgnores, ",")
	repo := t.TempDir()

	t.Run("no setting", func(t *testing.T) {
		first := gitignoreNeeded("", repo)
		if strings.Join(first, ",") != tracked+","+globs {
			t.Fatalf("first scaffold = %v", first)
		}
		for _, l := range first {
			if isSatelleWholesaleIgnore(l) {
				t.Errorf("the needed set must never ignore .satelle wholesale: %v", first)
			}
		}
		if second := gitignoreNeeded(strings.Join(first, "\n")+"\n", repo); len(second) != 0 {
			t.Errorf("second run must add nothing, got %v", second)
		}
	})
	t.Run("empty file equals missing file", func(t *testing.T) {
		if got := gitignoreNeeded("\n", repo); strings.Join(got, ",") != tracked+","+globs {
			t.Errorf("empty text = %v", got)
		}
	})
	t.Run("own wholesale ignore is left to the operator", func(t *testing.T) {
		got := gitignoreNeeded("/.satelle\n", repo)
		if strings.Join(got, ",") != strings.Join(satelleTrackedSubstrateIgnores[1:], ",")+","+globs {
			t.Errorf("an own .satelle rule gets no re-include over it, got %v", got)
		}
	})
	t.Run("old local.toml line is not the glob", func(t *testing.T) {
		got := gitignoreNeeded(".satelle/satelle.local.toml\n", repo)
		if !strings.Contains(strings.Join(got, ","), ".satelle/*.local.toml") {
			t.Errorf("the *.local.toml glob must be added, got %v", got)
		}
	})
	t.Run("tracked files", func(t *testing.T) {
		r := gitInitRepo(t, t.TempDir())
		writeFile(t, r, ".satelle/workflows/agents.toml", "[executor]\n")
		gitIn(t, r, "add", "-f", ".satelle")
		gitIn(t, r, "commit", "-qm", "track process")
		got := gitignoreNeeded("", r)
		if strings.Join(got, ",") != tracked+","+globs {
			t.Errorf("tracked .satelle gets the same shape, got %v", got)
		}
	})
}

// TestGitignoreNeededHarness (sty_dbc0be85 AC3): a harness dir present in the
// repo is ignored by default, once, unless the operator already has a setting
// for it or git tracks files under it.
func TestGitignoreNeededHarness(t *testing.T) {
	settledSatelle := strings.Join(satelleTrackedSubstrateIgnores, "\n") + "\n" + strings.Join(managedEditExemptGlobs, "\n") + "\n"
	for _, dir := range []string{".claude", ".grok"} {
		mk := func(t *testing.T) string {
			r := gitInitRepo(t, t.TempDir())
			if err := os.MkdirAll(filepath.Join(r, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, r, dir+"/settings.json", "{}\n")
			return r
		}
		t.Run(dir+" no setting", func(t *testing.T) {
			r := mk(t)
			got := gitignoreNeeded(settledSatelle, r)
			if strings.Join(got, ",") != dir+"/" {
				t.Fatalf("got %v, want %s/", got, dir)
			}
			if again := gitignoreNeeded(settledSatelle+dir+"/\n", r); len(again) != 0 {
				t.Errorf("second run must add nothing, got %v", again)
			}
		})
		t.Run(dir+" ignored", func(t *testing.T) {
			if got := gitignoreNeeded(settledSatelle+"/"+dir+"/\n", mk(t)); len(got) != 0 {
				t.Errorf("got %v", got)
			}
		})
		t.Run(dir+" negation", func(t *testing.T) {
			text := settledSatelle + dir + "/*\n!" + dir + "/settings.json\n"
			if got := gitignoreNeeded(text, mk(t)); len(got) != 0 {
				t.Errorf("got %v", got)
			}
		})
		t.Run(dir+" tracked", func(t *testing.T) {
			r := mk(t)
			gitIn(t, r, "add", "-f", dir)
			gitIn(t, r, "commit", "-qm", "commit the harness dir")
			if got := gitignoreNeeded(settledSatelle, r); len(got) != 0 {
				t.Errorf("a committed harness dir is left alone, got %v", got)
			}
		})
	}
	t.Run("absent dir", func(t *testing.T) {
		r := gitInitRepo(t, t.TempDir())
		if got := gitignoreNeeded(settledSatelle, r); len(got) != 0 {
			t.Errorf("no harness dir present, got %v", got)
		}
	})
}

// TestGitignoreHelpDescribesAddOnly (sty_dbc0be85 AC6): init and migrate help
// describe the add-only behaviour, not a rewrite of the block.
func TestGitignoreHelpDescribesAddOnly(t *testing.T) {
	for _, name := range []string{"init", "migrate"} {
		var long string
		for _, c := range NewRootCmd().Commands() {
			if c.Name() == name {
				long = c.Long
			}
		}
		if long == "" {
			t.Fatalf("%s command missing", name)
		}
		for _, banned := range []string{"rewritten between its markers", "rewrite the managed .gitignore block"} {
			if strings.Contains(long, banned) {
				t.Errorf("%s help still claims %q", name, banned)
			}
		}
		if !strings.Contains(long, "missing") {
			t.Errorf("%s help must say only missing entries are added:\n%s", name, long)
		}
	}
}

// wantTrackedSubstrateLines is AC1's tracked-substrate shape, spelled out so
// the init tests below pin the written block rather than read it back from
// the code under test.
var wantTrackedSubstrateLines = []string{
	"!.satelle/",
	".satelle/**/.env",
	".satelle/logs",
	".satelle/hooks",
	".satelle/deployed.version",
	".satelle/*.local.toml",
	".satelle/workflows/agents.local.toml",
	".satelle/satelle",
	".satelle/tasks/*/exe_*.md",
}

// gitIgnores reports whether git ignores rel in repo under its full rule set.
func gitIgnores(t *testing.T, repo, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "check-ignore", "-q", "--no-index", "--", rel)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false
	} else if err != nil {
		t.Fatalf("git check-ignore %s: %v", rel, err)
	}
	return true
}

// isolateGitExcludes points git's global config at a temp file that sets
// core.excludesFile to a file holding excludes (none when empty), so neither
// the operator's ~/.gitconfig nor ~/.config/git/ignore reaches the test.
func isolateGitExcludes(t *testing.T, excludes string) (excludesPath string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	body := ""
	if excludes != "" {
		excludesPath = filepath.Join(dir, "global-ignore")
		if err := os.WriteFile(excludesPath, []byte(excludes), 0o644); err != nil {
			t.Fatal(err)
		}
		body = "[core]\n\texcludesFile = " + excludesPath + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	return excludesPath
}

// TestInitWritesTrackedSubstrateBlock (sty_45bf867b AC1): on a fresh repo init
// writes the tracked-substrate block — the re-include, every local-state
// ignore, never .satelle/ itself — with a header naming git as the
// substrate's home, and a second run leaves the file byte-identical.
func TestInitWritesTrackedSubstrateBlock(t *testing.T) {
	isolateGitExcludes(t, "")
	repo := gitInitRepo(t, t.TempDir())
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	block := managedBlock(t, repo)
	for _, want := range wantTrackedSubstrateLines {
		if !hasLine(block, want) {
			t.Errorf("managed block lacks %q:\n%s", want, strings.Join(block, "\n"))
		}
	}
	text := strings.Join(block, "\n")
	if gitignoreTextIgnoresSatelleDir(text) {
		t.Errorf("init must never add .satelle/ itself:\n%s", text)
	}
	if !strings.Contains(text, "tracked in git") {
		t.Errorf("block comments must name git as the substrate's home:\n%s", text)
	}
	for _, retired := range []string{"rehydrate", "git-optional"} {
		if strings.Contains(text, retired) {
			t.Errorf("block comments carry retired wording %q:\n%s", retired, text)
		}
	}
	if gitIgnores(t, repo, ".satelle/constitution.md") || gitIgnores(t, repo, ".satelle/satelle.toml") {
		t.Error("the process substrate must not be ignored after init")
	}
	for _, local := range []string{".satelle/satelle.local.toml", ".satelle/hooks/satelle-hook.sh", ".satelle/tasks/tsk_x/exe_1.md", ".satelle/deployed.version"} {
		if !gitIgnores(t, repo, local) {
			t.Errorf("machine-local %s must stay ignored", local)
		}
	}
	before, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	out.Reset()
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("second init: %v\n%s", err, out.String())
	}
	after, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(before) != string(after) {
		t.Errorf("second init changed .gitignore:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// TestInitRetiresOldBlock (sty_45bf867b AC1): a repo carrying the old managed
// block — .satelle/ ignored wholesale under the git-optional / rehydrate
// header — is rewritten to the tracked-substrate block: the wholesale ignore
// and retired header go, the block's other rules stay, and every line outside
// the block is byte-for-byte as it was.
func TestInitRetiresOldBlock(t *testing.T) {
	isolateGitExcludes(t, "")
	repo := gitInitRepo(t, t.TempDir())
	head := "node_modules/\n"
	tail := "# keep-me\nbuild/\n"
	old := head + gitignoreMarker + "\n" +
		"# RECOMMENDED defaults — the operator owns .gitignore. .satelle is git-optional\n" +
		"# overall (continuity is local disk or personal rehydrate).\n" +
		".satelle/\n.satelle/satelle.local.toml\nsty_*_body.md\n" +
		gitignoreMarkerEnd + "\n" + tail
	writeFile(t, repo, ".gitignore", old)
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	gi := string(raw)
	if !strings.HasPrefix(gi, head+gitignoreMarker) || !strings.HasSuffix(gi, gitignoreMarkerEnd+"\n"+tail) {
		t.Fatalf("lines outside the block must be unchanged:\n%s", gi)
	}
	block := managedBlock(t, repo)
	text := strings.Join(block, "\n")
	if gitignoreTextIgnoresSatelleDir(gi) {
		t.Errorf("the old wholesale .satelle/ ignore must be removed:\n%s", gi)
	}
	for _, retired := range []string{"rehydrate", "git-optional"} {
		if strings.Contains(text, retired) {
			t.Errorf("retired header wording %q must go:\n%s", retired, text)
		}
	}
	for _, want := range append([]string{".satelle/satelle.local.toml", "sty_*_body.md"}, wantTrackedSubstrateLines...) {
		if !hasLine(block, want) {
			t.Errorf("block lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(out.String(), "removed .satelle/ from the managed block") {
		t.Errorf("init must report the removal:\n%s", out.String())
	}
	if gitIgnores(t, repo, ".satelle/satelle.toml") {
		t.Error("the process substrate must not be ignored after init")
	}
	if _, _, changed := convergeGitignore(gi, repo); changed {
		t.Errorf("a refreshed block must be settled:\n%s", gi)
	}
}

// TestInitOverridesExcludesFile (sty_45bf867b AC1): on a machine whose
// core.excludesFile ignores .satelle/, the block's re-include makes the
// substrate trackable while its local state stays ignored.
func TestInitOverridesExcludesFile(t *testing.T) {
	isolateGitExcludes(t, ".satelle/\n")
	repo := gitInitRepo(t, t.TempDir())
	writeFile(t, repo, ".satelle/satelle.toml", "")
	if !gitIgnores(t, repo, ".satelle/satelle.toml") {
		t.Fatal("test premise: the excludes file ignores .satelle/")
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	if gitIgnores(t, repo, ".satelle/satelle.toml") || gitIgnores(t, repo, ".satelle/constitution.md") {
		t.Error("the block's re-include must override core.excludesFile")
	}
	if !gitIgnores(t, repo, ".satelle/satelle.local.toml") {
		t.Error(".satelle/satelle.local.toml must stay ignored")
	}
}

// TestInitAllListsRetiredGitignoreBlock (sty_45bf867b AC1): init --all lists a
// registered repo whose only drift is the old .satelle/ block as STALE, heals
// it with --yes, and counts a repo whose block is current as current.
func TestInitAllListsRetiredGitignoreBlock(t *testing.T) {
	isolateUserHome(t)
	stale, current := t.TempDir(), t.TempDir()
	for _, r := range []string{stale, current} {
		if err := runInit(io.Discard, r, false, []string{"claude"}); err != nil {
			t.Fatalf("runInit %s: %v", r, err)
		}
		if fs := DetectScaffoldDrift(r); len(fs) != 0 {
			t.Fatalf("test premise: a freshly initialised repo is current: %v", fs)
		}
	}
	giPath := filepath.Join(stale, ".gitignore")
	raw, _ := os.ReadFile(giPath)
	old := strings.Replace(string(raw), "\n!.satelle/\n", "\n.satelle/\n", 1)
	if old == string(raw) {
		t.Fatalf("test premise: the fresh block re-includes .satelle/:\n%s", raw)
	}
	if err := os.WriteFile(giPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runInitAll(&out, false, nil); err != nil {
		t.Fatalf("init --all: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "STALE   "+stale+" — .gitignore managed block not current") {
		t.Errorf("the old-block repo must be listed STALE for its .gitignore:\n%s", out.String())
	}
	if strings.Contains(out.String(), "STALE   "+current) || !strings.Contains(out.String(), "1 already current") {
		t.Errorf("the current repo must count as current:\n%s", out.String())
	}
	if after, _ := os.ReadFile(giPath); string(after) != old {
		t.Error("a dry run must not change .gitignore")
	}

	out.Reset()
	if err := runInitAll(&out, true, nil); err != nil {
		t.Fatalf("init --all --yes: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "HEALED  "+stale) {
		t.Errorf("the old-block repo must be healed:\n%s", out.String())
	}
	healed, _ := os.ReadFile(giPath)
	if gitignoreTextIgnoresSatelleDir(string(healed)) || !strings.Contains(string(healed), "\n!.satelle/\n") {
		t.Errorf("heal must write the tracked-substrate block:\n%s", healed)
	}
}
