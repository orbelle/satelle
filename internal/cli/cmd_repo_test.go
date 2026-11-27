package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

// repoControl inits a control repository at dir (the working directory when
// dir is empty) and seats session on a story at a tree-editing state there.
func repoControl(t *testing.T, dir, session string) string {
	t.Helper()
	at := []string{}
	if dir == "" {
		dir = isolate(t)
	} else {
		gittest.Git(t, dir, "init", "-q")
		gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
		at = []string{"-C", dir}
	}
	must(t, append(at, "init")...)
	writePlanWorkflow(t, dir)
	id := createID(t, append(at, "story", "create", "--title", "repos", "--workflow", "planned")...)
	must(t, append(at, "story", "set", id, "--status", "st-plan")...)
	must(t, append(at, "story", "set", id, "--status", "st-build")...)
	must(t, append(at, "story", "seat", id, "--session", session)...)
	return dir
}

func checkoutWith(t *testing.T, origin string) string {
	t.Helper()
	dir := canonical(t.TempDir())
	gittest.Git(t, dir, "init", "-q")
	if origin != "" {
		gittest.Git(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func repoList(t *testing.T, args ...string) map[string]repoRow {
	t.Helper()
	var got struct{ Repos []repoRow }
	if err := json.Unmarshal([]byte(must(t, append([]string{"repo", "list", "--json"}, args...)...)), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]repoRow{}
	for i, r := range got.Repos {
		if i == 0 && r.Name != reporeg.Self {
			t.Errorf("first row %q, want self", r.Name)
		}
		out[r.Name] = r
	}
	return out
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestRepoAddRecordsTrackedAndLocalAndListShowsSelf(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	co := checkoutWith(t, "git@github.com:o/r.git")
	out := must(t, "repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co, "--session", "s-1")
	if !strings.Contains(out, "registered r") || !strings.Contains(out, "mapped r to "+co) {
		t.Errorf("add output:\n%s", out)
	}
	var tracked struct{ Repos map[string]map[string]string }
	if _, err := toml.DecodeFile(filepath.Join(dir, ".satelle", "repos.toml"), &tracked); err != nil {
		t.Fatal(err)
	}
	if tracked.Repos["r"]["origin"] != "https://github.com/o/r" || tracked.Repos["r"]["path"] != "" || len(tracked.Repos["r"]) != 1 {
		t.Errorf("repos.toml: %v", tracked.Repos)
	}
	var local struct{ Repos map[string]map[string]string }
	if _, err := toml.DecodeFile(filepath.Join(dir, ".satelle", "repos.local.toml"), &local); err != nil {
		t.Fatal(err)
	}
	if local.Repos["r"]["path"] != co {
		t.Errorf("repos.local.toml: %v", local.Repos)
	}
	if got := gittest.Git(t, dir, "check-ignore", ".satelle/repos.local.toml"); !strings.Contains(got, "repos.local.toml") {
		t.Errorf("repos.local.toml is not ignored: %q", got)
	}
	rows := repoList(t)
	if self := rows["self"]; self.Local.Path != dir || self.State != "ok" || self.Tracked.Tier != "standard" {
		t.Errorf("self row %+v", self)
	}
	if r := rows["r"]; r.Tracked.URL != "https://github.com/o/r" || r.Local.Path != co || r.State != "ok" || r.Tracked.Base != "main" {
		t.Errorf("r row %+v", r)
	}
	text := must(t, "repo", "list")
	for _, want := range []string{"NAME", "self", "https://github.com/o/r", co, "ok"} {
		if !strings.Contains(text, want) {
			t.Errorf("repo list lacks %q:\n%s", want, text)
		}
	}

	t.Run("teammate maps only the local file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, ".satelle", "repos.local.toml")); err != nil {
			t.Fatal(err)
		}
		before := readFileString(t, filepath.Join(dir, ".satelle", "repos.toml"))
		other := checkoutWith(t, "https://github.com/o/r.git")
		if _, err := run(t, "", "repo", "add", "r", "--origin", "https://github.com/o/elsewhere", "--path", other, "--session", "s-1"); err == nil ||
			!strings.Contains(err.Error(), "https://github.com/o/r") || !strings.Contains(err.Error(), "https://github.com/o/elsewhere") {
			t.Errorf("a differing --origin: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".satelle", "repos.local.toml")); !os.IsNotExist(err) {
			t.Error("a refused add wrote repos.local.toml")
		}
		must(t, "repo", "add", "r", "--path", other, "--session", "s-1")
		if after := readFileString(t, filepath.Join(dir, ".satelle", "repos.toml")); after != before {
			t.Errorf("the teammate flow changed repos.toml:\n%s", after)
		}
		if got := repoList(t)["r"]; got.Local.Path != other || got.State != "ok" {
			t.Errorf("after mapping: %+v", got)
		}
	})
}

func TestRepoAddRequiresTreeSeatAndHonoursLockedPaths(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	plan := createID(t, "story", "create", "--title", "planning", "--workflow", "planned")
	must(t, "story", "set", plan, "--status", "st-plan")
	must(t, "story", "seat", plan, "--session", "s-3")
	co := checkoutWith(t, "")
	files := []string{"repos.toml", "repos.local.toml", ".gitignore"}
	snapshot := func(root string) map[string]string {
		out := map[string]string{}
		for _, f := range files {
			out[f] = readFileString(t, filepath.Join(root, ".satelle", f))
		}
		return out
	}
	unchanged := func(t *testing.T, root string, before map[string]string) {
		t.Helper()
		for f, b := range snapshot(root) {
			if before[f] != b {
				t.Errorf(".satelle/%s changed", f)
			}
		}
	}
	add := func(extra ...string) error {
		_, err := run(t, "", append([]string{"repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co}, extra...)...)
		return err
	}
	second := repoControl(t, canonical(t.TempDir()), "s-9")
	for _, c := range []struct {
		name string
		args []string
		root string
		want string
	}{
		{"unseated", []string{"--session", "s-2"}, dir, "Seat one with"},
		{"attachments-only seat", []string{"--session", "s-3"}, dir, "produces an attachment"},
		{"no session", nil, dir, "no session id"},
		{"another control repo", []string{"-C", second, "--session", "s-1"}, second, "Seat one with"},
	} {
		before := snapshot(c.root)
		if err := add(c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		unchanged(t, c.root, before)
	}

	settings := filepath.Join(dir, ".satelle", "satelle.toml")
	orig := readFileString(t, settings)
	writeFile(t, settings, strings.Replace(orig, `locked_paths = ["locked/**"]`, `locked_paths = ["locked/**", ".satelle/repos.toml"]`, 1))
	before := snapshot(dir)
	if err := add("--session", "s-1"); err == nil || !strings.Contains(err.Error(), "locked path") {
		t.Errorf("locked repos.toml: %v", err)
	}
	unchanged(t, dir, before)
	writeFile(t, settings, orig)

	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	writeFile(t, outside, "# theirs\n")
	tracked := filepath.Join(dir, ".satelle", "repos.toml")
	if err := os.Symlink(outside, tracked); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := add("--session", "s-1"); err == nil || !strings.Contains(err.Error(), "outside the worktree") {
		t.Errorf("symlinked repos.toml: %v", err)
	}
	if readFileString(t, outside) != "# theirs\n" {
		t.Error("the symlink target changed")
	}
	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, ".satelle", "inside.toml")
	writeFile(t, inside, "")
	if err := os.Symlink(inside, filepath.Join(dir, ".satelle", "repos.local.toml")); err != nil {
		t.Fatal(err)
	}
	before = snapshot(dir)
	if err := add("--session", "s-1"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlinked repos.local.toml: %v", err)
	}
	unchanged(t, dir, before)
}

func TestRepoTierRecordedShownAndValidated(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	must(t, "repo", "add", "p", "--origin", "https://github.com/o/p", "--path", checkoutWith(t, "https://github.com/o/p"), "--tier", "protected", "--session", "s-1")
	must(t, "repo", "add", "s", "--origin", "https://github.com/o/s", "--path", checkoutWith(t, "https://github.com/o/s"), "--session", "s-1")
	body := readFileString(t, filepath.Join(dir, ".satelle", "repos.toml"))
	if !strings.Contains(body, `tier = "protected"`) || strings.Count(body, "tier") != 1 {
		t.Errorf("repos.toml:\n%s", body)
	}
	rows := repoList(t)
	if rows["p"].Tracked.Tier != "protected" || rows["s"].Tracked.Tier != "standard" {
		t.Errorf("tiers: p %q, s %q", rows["p"].Tracked.Tier, rows["s"].Tracked.Tier)
	}
	if _, err := run(t, "", "repo", "add", "x", "--origin", "https://github.com/o/x", "--path", t.TempDir(), "--tier", "secret", "--session", "s-1"); err == nil ||
		!strings.Contains(err.Error(), "tier is not standard or protected") {
		t.Errorf("--tier secret: %v", err)
	}
	writeFile(t, filepath.Join(dir, ".satelle", "repos.toml"), strings.Replace(body, `tier = "protected"`, `tier = "secret"`, 1))
	out, err := run(t, "", "validate")
	if err != nil {
		out += err.Error()
	}
	if err == nil || !strings.Contains(out, "repos.p") || !strings.Contains(out, "standard") || !strings.Contains(out, "protected") {
		t.Errorf("validate with tier secret: %v\n%s", err, out)
	}
	if rows := repoList(t); rows["p"].State != "invalid" || rows["p"].Tracked.URL != "-" {
		t.Errorf("an invalid entry: %+v", rows["p"])
	}
}

func TestQuoteControlQuotesFormatAndLineSeparatorRunes(t *testing.T) {
	for _, s := range []string{"a\u202eb", "a\u200bb", "a\u2028b", "a\x1b[31mb"} {
		if got := quoteControl(s); !strings.HasPrefix(got, `"`) {
			t.Errorf("quoteControl(%q) = %q, want it quoted", s, got)
		}
	}
	for _, s := range []string{"/plain/path", "café/ñ", "日本"} {
		if got := quoteControl(s); got != s {
			t.Errorf("quoteControl(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRepoGitCallsGetNoCredentials(t *testing.T) {
	repoControl(t, "", "s-1")
	co := checkoutWith(t, "https://github.com/o/r")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	logs := t.TempDir()
	writeExec(t, filepath.Join(bin, "git"), "#!/bin/sh\nf=$(mktemp '"+logs+"/call.XXXXXX')\n{ echo \"ARGS $*\"; env; } > \"$f\"\nexec '"+real+"' \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SATELLE_TEST_TOKEN", "tkn-x7")
	t.Setenv("GH_PASSWORD", "pw-x7")
	other := checkoutWith(t, "")
	registerProjects(t, other)
	must(t, "repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co, "--session", "s-1")
	must(t, "repo", "list")
	_, _ = run(t, "", "doctor")
	calls, err := filepath.Glob(filepath.Join(logs, "call.*"))
	if err != nil || len(calls) == 0 {
		t.Fatalf("the git wrapper logged nothing: %v", err)
	}
	seen := 0
	for _, c := range calls {
		got := readFileString(t, c)
		args, _, _ := strings.Cut(got, "\n")
		if !repoGitCall(args, other) {
			continue
		}
		seen++
		for _, secret := range []string{"tkn-x7", "pw-x7"} {
			if strings.Contains(got, secret) {
				t.Errorf("%s got %q in its environment", args, secret)
			}
		}
	}
	if seen == 0 {
		t.Error("no repo registry git call was logged")
	}
}

// repoGitCall is a git call the registry makes: a checkout's origin read, the
// repos.local.toml ignore and tracked reads, and another control repository's
// root lookup.
func repoGitCall(args, other string) bool {
	for _, c := range []string{"remote.origin.url", "repos.local.toml", "-C " + other + " "} {
		if strings.Contains(args, c) {
			return true
		}
	}
	return false
}
