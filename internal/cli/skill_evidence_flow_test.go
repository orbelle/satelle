package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_23ceeca2 end to end, through the real wiring: a story enters a step the
// driving session performs (the skill is delivered), the session runs its
// commands (the gate hooks write the trace), and leaving the step runs the
// skill-evidence check against that trace. One run satisfies the skill's
// declared command; one leaves it out and the omission is named.

const evidenceStepSkill = "---\nname: ev-step\ntype: skill\ndescription: a step skill that declares its evidence\nevidence_commands: go test\n---\n\n# Ev step\n\nRun the suite before you leave the step.\n"

func evidenceFlowRepo(t *testing.T, mode string) string {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	t.Setenv(config.SessionEnv, thisSession)
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "agents.toml"), []byte("[executor]\nharness = \"in-loop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "ev-step.md"), []byte(evidenceStepSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir, `["*"]
obligations = ["raised", "coded", "verified", "closed"]
`, `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
skills = ["ev-step"]
requires = ["raised"]

[verified]
status = "verifying"
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["verified"]
`)
	if mode != "" { // the route source's [meta] carries the strictness
		done := filepath.Join(wfDir, "done.toml")
		b, err := os.ReadFile(done)
		if err != nil {
			t.Fatal(err)
		}
		b = []byte(strings.Replace(string(b), "scope = \"project\"\n", "scope = \"project\"\nskill_evidence = \""+mode+"\"\n", 1))
		if err := os.WriteFile(done, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func newEvidenceStory(t *testing.T, repo string) string {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{
		"workflows": filepath.Join(repo, ".satelle", "workflows"),
		"skills":    filepath.Join(repo, ".satelle", "skills"),
	}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "evidence flow", Body: "goal",
		AcceptanceCriteria: "1. ok", Status: "backlog", Category: "chore",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return sty.ID
}

// evidenceVerdict returns the notes of the skill-evidence verdict row of a kind.
func evidenceVerdict(t *testing.T, kind string) (notes string, found bool) {
	t.Helper()
	for _, e := range ledgerRows(t, kind) {
		var p struct {
			Skill string `json:"skill"`
			Notes string `json:"notes"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.Skill == verb.SkillEvidenceCheck {
			return p.Notes, true
		}
	}
	return "", false
}
