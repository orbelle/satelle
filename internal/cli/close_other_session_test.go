package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
)

// closeDiffRunner is every reviewer on the close edge. It records the diff each
// payload references (the material file the reviewer reads); the diff rubric
// accepts only when that diff names the story's committed slice, and the reject
// rubric always rejects.
type closeDiffRunner struct {
	mu    sync.Mutex
	diffs []agentstep.DiffState
}

const closeRejectMarker = "ALWAYS-REJECT-RUBRIC"

func (*closeDiffRunner) Name() string    { return "close-diff-runner" }
func (*closeDiffRunner) Command() string { return "close-diff-runner -p {system}" }
func (r *closeDiffRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var top struct {
		Diff *struct {
			PatchPath string `json:"patch_path"`
		} `json:"diff"`
	}
	var diff agentstep.DiffState
	if json.Unmarshal([]byte(req.Payload), &top) == nil && top.Diff != nil {
		if b, err := os.ReadFile(top.Diff.PatchPath); err == nil {
			_ = json.Unmarshal(b, &diff)
		}
	}
	r.mu.Lock()
	r.diffs = append(r.diffs, diff)
	r.mu.Unlock()
	if strings.Contains(req.SystemPrompt, closeRejectMarker) {
		return []byte(`{"decision":"reject","notes":"the second gate rejects"}`), nil
	}
	for _, f := range diff.Files {
		if f == "slice.go" {
			return []byte(`{"decision":"accept","notes":"the change set is in the payload"}`), nil
		}
	}
	return []byte(`{"decision":"reject","notes":"the payload carries no change set"}`), nil
}

func (r *closeDiffRunner) seen() []agentstep.DiffState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agentstep.DiffState(nil), r.diffs...)
}

// otherSessionClose engages a story from a linked worktree T1 as owner-a and
// commits a slice there, then moves the process to the main tree T2 as
// release-task with no GateStoryEnv, and wires a reviewer engine whose close
// edge carries the named rubrics and the diff resolver the app wires.
func otherSessionClose(t *testing.T, reviewers ...string) (*closeEnv, string, *closeDiffRunner) {
	t.Helper()
	e := closeGuardEnv(t)
	wt := filepath.Join(t.TempDir(), "t1")
	e.git("worktree", "add", "-q", "-b", "t1", wt)
	if r, err := filepath.EvalSymlinks(wt); err == nil {
		wt = r
	}
	t.Setenv(verb.GateStoryEnv, "")
	t.Setenv("SATELLE_OWNER", "owner-a")
	t.Chdir(wt)
	id := e.engage()
	writeRepoFile(t, wt, "slice.go", "package slice\n")
	for _, args := range [][]string{{"add", "slice.go"}, {"commit", "-qm", "slice"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(e.repo)
	t.Setenv("SATELLE_OWNER", "release-task")

	meta := func(name string) string {
		return "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"close fixture\"\n\n"
	}
	quoted := make([]string, len(reviewers))
	for i, r := range reviewers {
		quoted[i] = `"` + r + `"`
	}
	step := strings.Replace(closeRouteStep, `requires = ["coded"]`,
		`requires = ["coded"]`+"\nreviewers = ["+strings.Join(quoted, ", ")+"]", 1)
	skill := func(name, body string) docindex.Doc {
		return docindex.Doc{Kind: "skills", Name: name, Body: "---\nname: " + name + "\ntype: skill\ndescription: close fixture\n---\n\n" +
			body + "\n\nReturn JSON {\"decision\": \"accept\"|\"reject\", \"notes\": \"…\"}.\n"}
	}
	docs := memDocs{docs: []docindex.Doc{
		{Kind: "workflows", Name: "done", Body: meta("done") + closeRouteDone},
		{Kind: "workflows", Name: "step", Body: meta("step") + step},
		skill("close-diff-review", "Accept only when the payload diff names the story's change set."),
		skill("close-reject-review", closeRejectMarker),
	}}
	r := &closeDiffRunner{}
	g := agentstep.New(r, docs, e.repo, "")
	g.SetDiffResolver(diffResolver())
	withVerbWiring(t)
	verb.SetTransitionGater(g)
	return e, id, r
}

// AC2: the reviewer payload a gate judges is the story's own change set when the
// close is presented from another tree and another session.
func TestDiffResolverReadsTheStoriesTreeFromAnotherSession(t *testing.T) {
	_, id, _ := otherSessionClose(t)
	d := diffResolver()(context.Background(), id)
	if d == nil || d.NoBaseline {
		t.Fatalf("the gate payload diff must resolve from another tree, got %+v", d)
	}
	if !hasString(d.Files, "slice.go") {
		t.Errorf("files = %v, want the slice committed in the story's own tree", d.Files)
	}
	// Unmarked, interactive enumeration from the same tree keeps the anchor.
	if _, err := verb.StoryDiff(context.Background(), id, verb.StoryDiffOpts{}); err == nil ||
		!strings.Contains(err.Error(), "was engaged from working tree") {
		t.Errorf("a plain story diff from another tree must still be refused: %v", err)
	}
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
