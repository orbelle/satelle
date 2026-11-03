package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_23ceeca2 AC1: entering a step the driving session performs — agent=executor,
// or a named agent bound in-loop — delivers the step's skills to that session:
// `story set` shows the rubric once, and the ledger records an in-loop row naming
// each skill delivered with the sha256 of its body.

const inLoopSkillBody = "---\nname: %s\ntype: skill\ndescription: an in-loop step skill\n---\n\n# %s\n\nFollow %s to the letter.\n"

func inLoopSkillsRepo(t *testing.T) (id string) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	t.Setenv(config.SessionEnv, thisSession)
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "agents.toml"), []byte("[executor]\nharness = \"in-loop\"\n\n[operator]\ncommand = \"in-loop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"exec-step", "operator-step"} {
		body := strings.ReplaceAll(inLoopSkillBody, "%s", n)
		if err := os.WriteFile(filepath.Join(skills, n+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "operated", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
skills = ["exec-step"]
requires = ["raised"]

[operated]
status = "operating"
agent = "operator"
skills = ["operator-step"]
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["operated"]
`)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir, "skills": skills}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "in-loop skill delivery", Body: "goal",
		AcceptanceCriteria: "1. ok", Status: "backlog", Category: "chore",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return sty.ID
}

// setStatus runs `story set --status` and returns what it wrote to stderr.
func setStatus(t *testing.T, id, status string) (stderr string, err error) {
	t.Helper()
	old := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	_, err = runRootIn(t, "", "story", "set", id, "--status", status)
	os.Stderr = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b), err
}
