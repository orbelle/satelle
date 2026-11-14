package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// realUpdateRefresh is update's own refresh spawn, before fakeInstall stubs it.
var realUpdateRefresh = updateRefresh

// staleSessionStart is a claude hook file's SessionStart holding the pre-v1
// satelle reindex beside a wrapped call and an echo that names satelle.
const staleSessionStart = `{"hooks":{"SessionStart":[{"hooks":[` +
	`{"type":"command","command":"satelle reindex"},` +
	`{"type":"command","command":"sh -c \"satelle reindex\""},` +
	`{"type":"command","command":"echo satelle is great"}]}]}}`

func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// harnessProject is a new satelle repo (a git repo with one commit and an
// initialised .satelle), registered on this machine when register is set.
func harnessProject(t *testing.T, register bool) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "init", "-q")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	if err := kernel.Init(filepath.Join(dir, config.DefaultDataDir)); err != nil {
		t.Fatal(err)
	}
	if register {
		gc, err := config.LoadGlobal()
		if err != nil {
			t.Fatal(err)
		}
		gc.Projects.Add(dir)
		if err := config.SaveGlobal(gc); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// staleHarness writes harness h's hook file in dir: satelle's current hooks
// plus a `satelle reindex` handler (for claude, also the wrapped call and the
// echo of staleSessionStart). It returns the file.
func staleHarness(t *testing.T, dir, h string) string {
	t.Helper()
	rel, _ := hook.File(h)
	path := filepath.Join(dir, filepath.FromSlash(rel))
	switch h {
	case hook.Cursor:
		putFile(t, path, `{"version":1,"hooks":{"sessionStart":[{"command":"satelle reindex"}]}}`)
	default:
		putFile(t, path, staleSessionStart)
	}
	if _, _, err := hook.Install(dir, h, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileState is a file's bytes and mtime, with the mtime first set an hour
// back so a rewrite shows.
type fileState struct {
	body []byte
	mod  time.Time
}

func pin(t *testing.T, path string) fileState {
	t.Helper()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileState{b, old}
}

func (f fileState) unchanged(t *testing.T, path string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	st, serr := os.Stat(path)
	return err == nil && serr == nil && bytes.Equal(b, f.body) && st.ModTime().Equal(f.mod)
}

func commandsOf(t *testing.T, dir, h string) []string {
	t.Helper()
	cmds, err := hook.Commands(dir, h)
	if err != nil {
		t.Fatal(err)
	}
	return cmds
}

// current reports whether cmds hold each of satelle's current hooks for h
// exactly once.
func current(cmds []string, h string) bool {
	for _, verb := range []string{hook.VerbGate, hook.VerbContext, hook.VerbPrompt, hook.VerbStopcheck} {
		n := 0
		for _, c := range cmds {
			if c == hook.Command(verb, h) {
				n++
			}
		}
		if n != 1 {
			return false
		}
	}
	return true
}

// AC1: a harness is in use when agents.toml runs its CLI or the tree has its
// configuration directory.
func TestHarnessesInUse(t *testing.T) {
	tree := t.TempDir()
	cat := agent.Catalog{
		"a": {Name: "a", Command: "cursor-agent --foo"},
		"b": {Name: "b", Command: "/opt/bin/grok"},
		"s": {Name: "s", Command: "session"},
	}
	if got := harnessesInUse(tree, cat); !slices.Equal(got, []string{hook.Grok, hook.Cursor}) {
		t.Errorf("by agents.toml: %q, want grok, cursor", got)
	}
	if got := harnessesInUse(tree, agent.Catalog{"s": {Name: "s", Command: "session"}}); len(got) != 0 {
		t.Errorf("a session binding and no directory: %q, want none", got)
	}
	putFile(t, filepath.Join(tree, ".claude"), "not a directory")
	if err := os.MkdirAll(filepath.Join(tree, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := harnessesInUse(tree, nil); !slices.Equal(got, []string{hook.Pi}) {
		t.Errorf("a bare .pi/ and a .claude file: %q, want pi", got)
	}
	r := repo{DataDir: filepath.Join(tree, ".satelle"), Tree: tree}
	putFile(t, filepath.Join(r.DataDir, kernel.AgentsFile), "[agents.x]\ncommand = \"claude\"\nbogus = 1\n")
	if got := harnessesInUse(tree, repoCatalog(r)); !slices.Equal(got, []string{hook.Pi}) {
		t.Errorf("an agents.toml that does not load: %q, want the directory harnesses (pi)", got)
	}
}

// AC2: agents refresh reviews every registered project from anywhere: it
// adds or updates satelle's entries in each harness in use, removes a
// first-word satelle command the binary lacks, keeps every other handler,
// and leaves an unregistered repo alone; a second run writes nothing.
func TestAgentsRefreshRewritesRegisteredProjects(t *testing.T) {
	isolate(t)
	a := harnessProject(t, true)
	aFile := staleHarness(t, a, hook.Claude)
	b := harnessProject(t, true)
	putFile(t, filepath.Join(b, ".satelle", kernel.AgentsFile), "[agents.c]\ncommand = \"cursor-agent\"\n")
	c := harnessProject(t, true)
	if err := os.MkdirAll(filepath.Join(c, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := harnessProject(t, false)
	otherFile := staleHarness(t, other, hook.Claude)
	otherState := pin(t, otherFile)

	out := must(t, "agents", "refresh")
	cmds := commandsOf(t, a, hook.Claude)
	if slices.Contains(cmds, "satelle reindex") || !slices.Contains(cmds, `sh -c "satelle reindex"`) ||
		!slices.Contains(cmds, "echo satelle is great") || !current(cmds, hook.Claude) {
		t.Errorf("repo A's claude hooks: %q", cmds)
	}
	if cmds := commandsOf(t, b, hook.Cursor); !current(cmds, hook.Cursor) {
		t.Errorf("repo B's cursor hooks (agents.toml runs cursor-agent): %q", cmds)
	}
	if cmds := commandsOf(t, c, hook.Grok); !current(cmds, hook.Grok) {
		t.Errorf("repo C's grok hooks (.grok/ present): %q", cmds)
	}
	for _, f := range []string{filepath.Join(b, ".claude"), filepath.Join(c, ".claude"), filepath.Join(c, ".cursor"), filepath.Join(a, ".pi")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s written for a harness not in use", f)
		}
	}
	if !otherState.unchanged(t, otherFile) {
		t.Error("the unregistered repo's hook file changed")
	}
	for _, want := range []string{"refreshed claude hooks: " + aFile, "refreshed cursor hooks: " + filepath.Join(b, ".cursor", "hooks.json"),
		"refreshed grok hooks: " + filepath.Join(c, ".grok", "hooks", "satelle.json"), "harness files: 3 refreshed across 3 project(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("agents refresh lacks %q:\n%s", want, out)
		}
	}
	aState := pin(t, aFile)
	if out := must(t, "agents", "refresh"); !strings.Contains(out, "harness files: 0 refreshed") {
		t.Errorf("second run:\n%s", out)
	}
	if !aState.unchanged(t, aFile) {
		t.Error("the second run rewrote a current file")
	}
}

// AC3: [hook] refresh = false leaves the repo's files through agents
// refresh, update and the sync pass; refresh and update name the repo.
func TestRefreshLeavesAnOptedOutRepo(t *testing.T) {
	isolate(t)
	dir := harnessProject(t, true)
	writeHookSettings(t, dir, "refresh = false\n")
	file := staleHarness(t, dir, hook.Claude)
	state := pin(t, file)
	fakeInstall(t, false)
	releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
	for _, args := range [][]string{{"agents", "refresh"}, {"update"}, {"ledger", "autosync"}} {
		out, err := run(t, "", args...)
		if err != nil && args[0] != "ledger" {
			t.Fatalf("%s: %v\n%s", args, err, out)
		}
		if !state.unchanged(t, file) {
			t.Fatalf("%s rewrote the opted-out repo's file", args)
		}
		if args[0] != "ledger" && !strings.Contains(out, "left "+dir+": [hook] refresh = false") {
			t.Errorf("%s does not name the opted-out repo:\n%s", args, out)
		}
	}
}

// trackedProject is a registered repo whose committed .claude/settings.json
// and .cursor/hooks.json each hold satelle's hooks plus satelle reindex.
func trackedProject(t *testing.T) (dir string, files []string) {
	t.Helper()
	dir = harnessProject(t, true)
	files = []string{staleHarness(t, dir, hook.Claude), staleHarness(t, dir, hook.Cursor)}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "hooks")
	return dir, files
}

// AC5: agents refresh, update and the sync pass never rewrite a hook file
// git tracks; refresh and update name it with the install command.
func TestTrackedHarnessFilesAreNeverRewritten(t *testing.T) {
	for _, args := range [][]string{{"agents", "refresh"}, {"update"}, {"ledger", "autosync"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolate(t)
			dir, files := trackedProject(t)
			states := []fileState{pin(t, files[0]), pin(t, files[1])}
			fakeInstall(t, false)
			releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
			out, err := run(t, "", args...)
			if err != nil && args[0] != "ledger" {
				t.Fatalf("%v\n%s", err, out)
			}
			for i, f := range files {
				if !states[i].unchanged(t, f) {
					t.Errorf("%s rewrote the tracked %s", args, f)
				}
			}
			if st := gittest.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
				t.Errorf("git status after %s:\n%s", args, st)
			}
			if args[0] == "ledger" {
				// The sync pass does not print tracked files every 30 s
				// (plan: doctor reports them). Prove the pass did reach the
				// refresh, so "unchanged" above is not vacuous: an untracked
				// stale harness file in the same repo is refreshed by a pass
				// over the same tree, which still leaves the tracked ones.
				if err := os.MkdirAll(filepath.Join(dir, ".grok"), 0o755); err != nil {
					t.Fatal(err)
				}
				grok := staleHarness(t, dir, hook.Grok)
				gittest.Git(t, dir, "rm", "-q", "--cached", "--ignore-unmatch", grok)
				out, _ := run(t, "", args...)
				if !strings.Contains(out, grok+": refreshed grok hooks") {
					t.Fatalf("the sync pass did not refresh the untracked %s:\n%s", grok, out)
				}
				for i, f := range files {
					if !states[i].unchanged(t, f) {
						t.Errorf("%s rewrote the tracked %s on the pass that refreshed %s", args, f, grok)
					}
				}
				return
			}
			for i, h := range []string{hook.Claude, hook.Cursor} {
				if want := "left " + files[i] + ": git tracks it; run 'satelle agents install " + h + "'"; !strings.Contains(out, want) {
					t.Errorf("%s lacks %q:\n%s", args, want, out)
				}
			}
		})
	}
}

// AC5: when git cannot say whether a file is tracked, nothing is written and
// agents refresh fails.
func TestRefreshFailsClosedWhenGitCannotAnswer(t *testing.T) {
	isolate(t)
	dir, files := trackedProject(t)
	putFile(t, filepath.Join(dir, ".git", "index"), "garbage")
	states := []fileState{pin(t, files[0]), pin(t, files[1])}
	out, err := run(t, "", "agents", "refresh")
	if err == nil || ExitCodeOf(err) != ExitError || !strings.Contains(out, "cannot tell whether git tracks it") {
		t.Errorf("agents refresh over an unreadable index: %v\n%s", err, out)
	}
	_, _ = run(t, "", "ledger", "autosync")
	for i, f := range files {
		if !states[i].unchanged(t, f) {
			t.Errorf("%s was written though git could not answer", f)
		}
	}
}

// AC7: agents install applies the same rule by hand, against this binary's
// command tree.
func TestAgentsInstallAppliesTheRefreshRule(t *testing.T) {
	isolate(t)
	dir := harnessProject(t, false)
	t.Chdir(dir)
	path := filepath.Join(dir, ".claude", "settings.json")
	gone := []string{"satelle reindex", "/usr/local/bin/satelle reindex"}
	kept := []string{"satelle doctor", `sh -c "satelle reindex"`, "FOO=1 satelle reindex", "timeout 5 satelle reindex", "echo satelle is great", "echo hi"}
	var hs []any
	for _, c := range append(slices.Clone(gone), kept...) {
		hs = append(hs, map[string]any{"type": "command", "command": c})
	}
	body, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": hs}}}})
	putFile(t, path, string(body))
	if out := must(t, "agents", "install", "claude"); !strings.Contains(out, "installed claude hooks: "+path) {
		t.Fatalf("agents install:\n%s", out)
	}
	cmds := commandsOf(t, dir, hook.Claude)
	for _, g := range gone {
		if slices.Contains(cmds, g) {
			t.Errorf("%q kept", g)
		}
	}
	for _, k := range kept {
		if !slices.Contains(cmds, k) {
			t.Errorf("%q removed", k)
		}
	}
	if !current(cmds, hook.Claude) {
		t.Errorf("not the current set: %q", cmds)
	}
	state := pin(t, path)
	if out := must(t, "agents", "install", "claude"); !strings.Contains(out, "claude hooks are current: "+path) {
		t.Errorf("second install:\n%s", out)
	}
	if !state.unchanged(t, path) {
		t.Error("the second install rewrote a current file")
	}
}

// AC7: on a tracked file agents install writes the working tree only and
// says the change is to be committed in that repo.
func TestInstallOnATrackedFileLeavesTheCommitToTheRepo(t *testing.T) {
	isolate(t)
	dir, _ := trackedProject(t)
	t.Chdir(dir)
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	out := must(t, "agents", "install", "claude")
	if slices.Contains(commandsOf(t, dir, hook.Claude), "satelle reindex") {
		t.Error("satelle reindex kept")
	}
	if staged, unstaged := gittest.Git(t, dir, "diff", "--cached", "--name-only"), gittest.Git(t, dir, "diff", "--name-only"); staged != "" || unstaged != ".claude/settings.json" {
		t.Errorf("staged %q, unstaged %q; want only .claude/settings.json, unstaged", staged, unstaged)
	}
	if got := gittest.Git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Error("HEAD moved")
	}
	if !strings.Contains(out, "git tracks this file") || !strings.Contains(out, "commit it in this repo") {
		t.Errorf("agents install on a tracked file:\n%s", out)
	}
	untracked := harnessProject(t, false)
	t.Chdir(untracked)
	if out := must(t, "agents", "install", "claude"); strings.Contains(out, "git tracks") {
		t.Errorf("agents install on an untracked file:\n%s", out)
	}
}

// AC2: update refreshes the registered projects' hook files: in-process when
// the CLI is current, through the replaced CLI's agents refresh when not; a
// refresh that cannot finish makes update exit 1 naming agents refresh,
// whatever the service opt-out.
func TestUpdateRefreshesHarnessFiles(t *testing.T) {
	t.Run("current CLI", func(t *testing.T) {
		isolate(t)
		dir := harnessProject(t, true)
		staleHarness(t, dir, hook.Claude)
		fakeInstall(t, false)
		releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
		out := must(t, "update", "--no-service")
		if cmds := commandsOf(t, dir, hook.Claude); slices.Contains(cmds, "satelle reindex") || !current(cmds, hook.Claude) {
			t.Errorf("after update: %q\n%s", cmds, out)
		}
	})
	for _, tc := range []struct{ name, env, flag string }{
		{"refresh fails, --no-service", "", "--no-service"},
		{"refresh fails, SATELLE_NO_SERVICE=1", "1", ""},
		{"refresh fails, service step runs", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("the fake systemctl and satelled are shell scripts")
			}
			isolate(t)
			t.Setenv(envNoService, tc.env)
			fakeSystemctl(t, "")
			dir, files := trackedProject(t)
			putFile(t, filepath.Join(dir, ".git", "index"), "garbage")
			state := pin(t, files[0])
			fakeInstall(t, true)
			releaseServer(t, "v"+buildinfo.Version, []string{"serve-v0.1.0"}, nil, "")
			args := []string{"update"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			out, err := run(t, "", args...)
			if err == nil || ExitCodeOf(err) != ExitError || !strings.Contains(err.Error(), "harness files are not refreshed") ||
				!strings.Contains(err.Error(), "satelle agents refresh") {
				t.Errorf("update: %v\n%s", err, out)
			}
			if !state.unchanged(t, files[0]) {
				t.Error("the file was written")
			}
		})
	}
	t.Run("replaced CLI runs its own agents refresh", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the new CLI is a shell script")
		}
		isolate(t)
		cli, _ := fakeInstall(t, false)
		updateRefresh = realUpdateRefresh
		marker := filepath.Join(t.TempDir(), "argv")
		releaseServer(t, "v9.9.9", nil, map[string]string{
			"v9.9.9/" + asset("satelle", "9.9.9"): "#!/bin/sh\necho \"$@\" > " + marker + "\n",
		}, "")
		out := must(t, "update", "--no-service")
		if got := strings.TrimSpace(readString(t, marker)); got != "agents refresh" {
			t.Errorf("the new CLI ran %q, want agents refresh\n%s", got, out)
		}
		if !strings.HasPrefix(readString(t, cli), "#!/bin/sh") {
			t.Error("the CLI was not replaced")
		}
	})
	t.Run("replaced CLI that cannot run", func(t *testing.T) {
		isolate(t)
		cli, _ := fakeInstall(t, false)
		updateRefresh = realUpdateRefresh
		releaseServer(t, "v9.9.9", nil, map[string]string{"v9.9.9/" + asset("satelle", "9.9.9"): "not a program"}, "")
		_, err := run(t, "", "update", "--no-service")
		if err == nil || !strings.Contains(err.Error(), "satelle agents refresh") {
			t.Errorf("update: %v", err)
		}
		if readString(t, cli) != "not a program" {
			t.Error("the CLI was not replaced")
		}
	})
}

// AC8: one sync pass refreshes a registered project's untracked hook files;
// a second writes nothing.
func TestAutosyncRefreshesHarnessFiles(t *testing.T) {
	isolate(t)
	dir := harnessProject(t, true)
	file := staleHarness(t, dir, hook.Claude)
	out, _ := run(t, "", "ledger", "autosync")
	if cmds := commandsOf(t, dir, hook.Claude); slices.Contains(cmds, "satelle reindex") || !current(cmds, hook.Claude) {
		t.Fatalf("after the pass: %q\n%s", cmds, out)
	}
	if !strings.Contains(out, file+": refreshed claude hooks") {
		t.Errorf("the pass does not name the file:\n%s", out)
	}
	state := pin(t, file)
	_, _ = run(t, "", "ledger", "autosync")
	if !state.unchanged(t, file) {
		t.Error("the second pass rewrote a current file")
	}
}

// A registered pre-1.0 repo is left to satelle upgrade --archive: agents
// refresh names it, writes nothing there and does not fail (sto_0f76dd4e).
func TestAgentsRefreshLeavesAPreV1Repo(t *testing.T) {
	isolate(t)
	old := harnessProject(t, true)
	putFile(t, filepath.Join(old, config.DefaultDataDir, "workflows", "step.toml"), "[meta]\nname = \"step\"\n")
	file := staleHarness(t, old, hook.Claude)
	state := pin(t, file)
	out := must(t, "agents", "refresh")
	if !state.unchanged(t, file) {
		t.Error("the pre-1.0 repo's hook file changed")
	}
	if want := "left " + old + ": pre-1.0 .satelle; run '" + kernel.UpgradeCommand + "' there"; !strings.Contains(out, want) {
		t.Errorf("agents refresh lacks %q:\n%s", want, out)
	}
}
