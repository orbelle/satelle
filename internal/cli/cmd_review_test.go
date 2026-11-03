package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/tests/reviewcorpus"
)

// stubBindings swaps the judge factory for named stub bindings: each answers a
// case by its expected verdict, or the opposite when it is listed in wrong.
func stubBindings(t *testing.T, wrong map[string]bool) {
	t.Helper()
	prev := newReviewJudge
	t.Cleanup(func() { newReviewJudge = prev })
	newReviewJudge = func(_ *cobra.Command, binding string) (reviewscore.Judge, string, error) {
		if binding == "missing" {
			return nil, "", os.ErrNotExist
		}
		return reviewscore.JudgeFunc(func(_ context.Context, c reviewscore.Case) (reviewscore.JudgeResult, error) {
			v := c.ExpectedVerdict
			if wrong[binding] {
				if v == reviewscore.VerdictReject {
					v = reviewscore.VerdictAccept
				} else {
					v = reviewscore.VerdictReject
				}
			}
			return reviewscore.JudgeResult{
				Verdict: v, Notes: "stub notes citing the missing acceptance criteria", Wall: time.Second,
				Usage: reviewscore.Usage{Reason: "unavailable (stubcli: no usage in plain text)"},
			}, nil
		}), "stubcli", nil
	}
}

// reviewFixtureStory creates a story with a recorded review_reject row and a
// later title edit, and returns its id and the review row's id.
func reviewFixtureStory(t *testing.T) (storyID, reviewID string) {
	t.Helper()
	out, err := runRoot(t, "story", "create", "--title", "Title now", "--body", "Body now", "--status", "backlog")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	add := func(kind string, payload map[string]any) ledger.Entry {
		b, _ := json.Marshal(payload)
		e, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: created.ID, Kind: kind, Actor: "reviewer", Body: kind, Payload: b}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	rej := add(ledger.KindReviewReject, map[string]any{
		"from": "backlog", "to": "plan", "skill": "satelle-story-intent-review", "notes": "the story has no acceptance criteria", "accept": false,
	})
	add(ledger.KindDefinitionEdited, map[string]any{"field": "title", "old": "Title then", "new": "Title now"})
	return created.ID, rej.ID
}

func TestReviewCaptureScoreAndCompareEndToEndWithStubBindings(t *testing.T) {
	tempRepo(t)
	stubBindings(t, map[string]bool{"worse": true})
	storyID, reviewID := reviewFixtureStory(t)
	scratch := t.TempDir()
	replay := filepath.Join(scratch, "replay")

	// capture: AC2. The ledger keeps no payload, so without the reviewed change the
	// capture is refused rather than manufacturing a case with nothing to judge.
	if out, err := runRoot(t, "review", "capture", storyID, "--ledger", reviewID, "--expect", "reject", "--out", replay); err == nil ||
		!strings.Contains(err.Error(), "--patch") {
		t.Fatalf("capture without the reviewed change: err=%v\n%s", err, out)
	}
	patchFile := filepath.Join(scratch, "reviewed.patch")
	if err := os.WriteFile(patchFile, []byte("--- a/x\n+++ b/x\n+REVIEWED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "review", "capture", storyID, "--ledger", reviewID, "--expect", "reject", "--patch", patchFile,
		"--defect-marker", "acceptance criteria", "--out", replay)
	if err != nil {
		t.Fatalf("capture: %v\n%s", err, out)
	}
	cases, err := reviewscore.LoadReplay(replay)
	if err != nil || len(cases) != 1 {
		t.Fatalf("replay cases = %v, %v", cases, err)
	}
	c := cases[0]
	if c.PayloadSource != reviewscore.PayloadReconstructed || c.ExpectedVerdict != reviewscore.VerdictReject ||
		c.OriginalNotes != "the story has no acceptance criteria" || c.Skill != "satelle-story-intent-review" ||
		c.From != "backlog" || c.To != "plan" || c.Payload == nil || c.Payload.Title != "Title then" ||
		c.Payload.Patch != "--- a/x\n+++ b/x\n+REVIEWED\n" || c.Unfaithful() {
		t.Errorf("captured case = %+v payload=%+v", c, c.Payload)
	}

	// score: AC1 + AC2 (replay together with the corpus).
	corpus := reviewcorpus.Dir()
	good := filepath.Join(scratch, "good.json")
	out, err = runRoot(t, "review", "score", "--binding", "good", "--corpus", corpus, "--replay", replay, "--out", good)
	if err != nil {
		t.Fatalf("score: %v\n%s", err, out)
	}
	for _, want := range []string{"RECALL", "FALSE BLOCKERS", "ESCAPES", "FINDING MATCH", "TOTAL", "ready", "satelle-story-intent-review",
		"unavailable (stubcli: no usage in plain text)", "report: " + good} {
		if !strings.Contains(out, want) {
			t.Errorf("score output missing %q:\n%s", want, out)
		}
	}
	rep, err := reviewscore.ReadReport(good)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Binding != "good" || rep.Adapter != "stubcli" || rep.Total.Rows != len(rep.Cases) || rep.Total.Escapes != 0 || rep.Total.FalseBlockers != 0 {
		t.Errorf("good report total = %+v", rep.Total)
	}
	var replayRow *reviewscore.RowResult
	for i := range rep.Cases {
		if rep.Cases[i].Source == reviewscore.OriginReplay {
			replayRow = &rep.Cases[i]
		}
	}
	if replayRow == nil || replayRow.FindingMatch != reviewscore.FindingMatched || replayRow.PayloadSource != reviewscore.PayloadReconstructed {
		t.Errorf("replay row = %+v", replayRow)
	}

	// compare: AC3 — same case set side by side ...
	worse := filepath.Join(scratch, "worse.json")
	if out, err = runRoot(t, "review", "score", "--binding", "worse", "--corpus", corpus, "--replay", replay, "--out", worse); err != nil {
		t.Fatalf("score worse: %v\n%s", err, out)
	}
	out, err = runRoot(t, "review", "compare", good, worse)
	if err != nil {
		t.Fatalf("compare: %v\n%s", err, out)
	}
	for _, want := range []string{"GOOD", "WORSE", "recall", "false blockers", "escapes", "finding match", "wall time", "tokens", "cost", "TOTAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("compare output missing %q:\n%s", want, out)
		}
	}

	// ... and refused when the case sets differ.
	corpusOnly := filepath.Join(scratch, "corpus-only.json")
	if out, err = runRoot(t, "review", "score", "--binding", "good", "--corpus", corpus, "--out", corpusOnly); err != nil {
		t.Fatalf("score corpus only: %v\n%s", err, out)
	}
	out, err = runRoot(t, "review", "compare", good, corpusOnly)
	if err == nil || !strings.Contains(err.Error(), "not scored over the same case set") {
		t.Fatalf("compare over different case sets: err=%v out=%s", err, out)
	}
}

func TestReviewScoreRefusesWhatCannotBeScored(t *testing.T) {
	tempRepo(t)
	stubBindings(t, nil)
	corpus := reviewcorpus.Dir()
	cases := map[string][]string{
		"no binding":      {"review", "score", "--corpus", corpus},
		"nothing to run":  {"review", "score", "--binding", "x"},
		"unknown binding": {"review", "score", "--binding", "missing", "--corpus", corpus},
		"empty corpus":    {"review", "score", "--binding", "x", "--corpus", t.TempDir()},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if out, err := runRoot(t, args...); err == nil {
				t.Fatalf("expected an error, got:\n%s", out)
			}
		})
	}
}

func TestReviewCaptureRefusesANonReviewRow(t *testing.T) {
	tempRepo(t)
	storyID, _ := reviewFixtureStory(t)
	out, err := runRoot(t, "ledger", "list", "--story", storyID, "--kind", ledger.KindDefinitionEdited)
	if err != nil {
		t.Fatal(err)
	}
	var rows []ledger.Entry
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("rows = %v, %v\n%s", rows, err, out)
	}
	out, err = runRoot(t, "review", "capture", storyID, "--ledger", rows[0].ID, "--expect", "accept", "--out", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "needs a review_accept or review_reject row") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}
