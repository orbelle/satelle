//go:build integration && unix

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	existingFixture        = "testdata/singlerepo"
	existingFixtureRepo    = "single-repo"
	existingFixtureGolden  = existingFixture + "/results.golden"
	existingFixtureWrite   = "SATELLE_EXISTING_WRITE"
	existingCaptureCommand = existingFixtureWrite + "=1 go test -tags integration -run '^TestExistingSingleRepoProject$' ./tests/"
	existingStory          = "sto_2a540655"
	existingSeatStory      = "sto_35c66e93"
	existingFinishedStory  = "sto_bac39dbf"
	existingSession        = "s-existing"
)

type recordedStep struct {
	name string
	body string
}

func TestExistingSingleRepoProject(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(realDir(t, t.TempDir()), existingFixtureRepo)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newSandboxAt(t, dir)
	copyFixtureDir(t, filepath.Join(existingFixture, "repo", "satelle"), filepath.Join(s.dir, ".satelle"))
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "a single-repo project")

	key := existingRepoKey(t, s.dir)
	runtime := filepath.Join(s.env["SATELLE_HOME"], key)
	copyFixtureDir(t, filepath.Join(existingFixture, "runtime"), runtime)
	copyFixtureDir(t, filepath.Join(existingFixture, "home"), s.env["SATELLE_HOME"])

	foreign := filepath.Join(realDir(t, t.TempDir()), "other-repo")
	s.git("init", "-q", foreign)

	got := existingProjectSteps(t, s, foreign)
	head := strings.TrimSpace(s.git("rev-parse", "--short=12", "HEAD"))
	norm := strings.NewReplacer(runtime, "$HOME/.satelle/$KEY", key, "$KEY", s.dir, "$REPO", foreign, "$FOREIGN",
		s.home, "$HOME", filepath.Dir(satelleBin), "$BIN", head, "$HEAD")
	for i := range got {
		got[i].body = norm.Replace(got[i].body)
	}

	if os.Getenv(existingFixtureWrite) == "1" {
		writeRecordedSteps(t, got)
		return
	}
	want := readRecordedSteps(t)
	compareRecordedSteps(t, existingFixture, want, got)
}

func existingRepoKey(t *testing.T, repo string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(realDir(t, filepath.Join(repo, ".git"))))
	return filepath.Base(repo) + "-" + hex.EncodeToString(sum[:])[:8]
}

func existingProjectSteps(t *testing.T, s *sandbox, foreign string) []recordedStep {
	t.Helper()
	var steps []recordedStep
	cmd := func(name string, args ...string) {
		t.Helper()
		steps = append(steps, recordedStep{name, transcript(s.run(args...))})
	}
	cmd("repo key and runtime path", "runtime", "path")
	cmd("runtime list", "runtime", "list")
	cmd("story list", "story", "list")
	cmd("story get "+existingStory, "story", "get", existingStory)
	cmd("story get "+existingFinishedStory, "story", "get", existingFinishedStory)
	cmd("story attachment "+existingStory+" plan", "story", "attachment", existingStory, "plan")
	cmd("validate", "validate")
	cmd("ledger verify", "ledger", "verify")

	payload, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "session_id": existingSession, "cwd": s.dir})
	steps = append(steps, recordedStep{"session start", sessionStart(s.exec(satelleBin, string(payload), "hook", "context", "--harness", "claude"))})
	cmd("seat "+existingSeatStory, "story", "seat", existingSeatStory, "--session", existingSession)
	cmd("set "+existingSeatStory+" doing", "story", "set", existingSeatStory, "--status", "doing", "--session", existingSession)
	gate := func(name, session, file string) {
		t.Helper()
		deny, reason := s.gate(session, "Edit", file, "")
		decision := "allow"
		if deny {
			decision = "deny"
		}
		steps = append(steps, recordedStep{name, "decision: " + decision + "\nreason: " + reason + "\n"})
	}
	gate("edit gate, path inside the repo", existingSession, filepath.Join(s.dir, "main.go"))
	gate("edit gate, path in another repository", existingSession, filepath.Join(foreign, "main.go"))
	gate("edit gate, another session inside the repo", "s-other", filepath.Join(s.dir, "main.go"))

	steps = append(steps, recordedStep{"state files", stateFiles(t, s.env["SATELLE_HOME"])})
	return steps
}

var (
	machineCheckLine = regexp.MustCompile(`(?m)^\s*(` + strings.Join(machineChecks, "|") + `) \(.*\n`)
	doctorCount      = regexp.MustCompile(`satelle doctor: [0-9]+ issue\(s\)`)
)

func sessionStart(r result) string {
	var doc struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		System string `json:"systemMessage"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil {
		return transcript(r)
	}
	machine := func(s string) string {
		return doctorCount.ReplaceAllString(machineCheckLine.ReplaceAllString(s, ""), "satelle doctor: N issue(s)")
	}
	return "[context]\n" + machine(doc.Hook.Context) + "[system message]\n" + machine(doc.System) + transcript(result{err: r.err, code: r.code})
}

func transcript(r result) string {
	var b strings.Builder
	b.WriteString(r.out)
	if r.err != "" {
		b.WriteString("[stderr]\n" + r.err)
	}
	b.WriteString("[exit " + strconv.Itoa(r.code) + "]\n")
	return b.String()
}

func stateFiles(t *testing.T, root string) string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return strings.Join(files, "\n") + "\n"
}

func copyFixtureDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

const stepMark = "=== "

func writeRecordedSteps(t *testing.T, steps []recordedStep) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# The recorded results of the built satelle on the " + existingFixture + " fixture.\n")
	b.WriteString("# The fixture's ledger was written by satelle 1.0.149, the release before the external control-plane epic.\n")
	b.WriteString("# $REPO, $FOREIGN and $HOME stand for the sandbox paths; $KEY for <repo dir name>-<first 8 hex of sha256 of the repo's .git path>.\n")
	b.WriteString("# captured by: " + existingCaptureCommand + "\n")
	for _, st := range steps {
		b.WriteString(stepMark + st.name + "\n" + st.body)
	}
	if err := os.WriteFile(existingFixtureGolden, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d steps", existingFixtureGolden, len(steps))
}

func readRecordedSteps(t *testing.T) []recordedStep {
	t.Helper()
	b, err := os.ReadFile(existingFixtureGolden)
	if err != nil {
		t.Fatalf("%v (capture it: %s)", err, existingCaptureCommand)
	}
	var steps []recordedStep
	for _, l := range strings.SplitAfter(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, stepMark):
			steps = append(steps, recordedStep{name: strings.TrimSuffix(strings.TrimPrefix(l, stepMark), "\n")})
		case len(steps) == 0 && strings.HasPrefix(l, "#"):
		case len(steps) > 0:
			steps[len(steps)-1].body += l
		}
	}
	return steps
}

func compareRecordedSteps(t *testing.T, fixture string, want, got []recordedStep) {
	t.Helper()
	names := func(ss []recordedStep) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.name)
		}
		return out
	}
	if !slices.Equal(names(want), names(got)) {
		t.Fatalf("fixture %s: the recorded steps differ from %s:\n%s", fixture, existingFixtureGolden, lineDiff(names(want), names(got)))
	}
	for i := range want {
		if want[i].body != got[i].body {
			t.Errorf("fixture %s: %q differs from the recorded result in %s:\n%s", fixture, want[i].name, existingFixtureGolden,
				lineDiff(strings.Split(want[i].body, "\n"), strings.Split(got[i].body, "\n")))
		}
	}
}
