//go:build clislow

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/throughput"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// The JSON and the text are one report: re-rendering the decoded JSON gives the
// text byte for byte, and an unavailable figure is null with its reason.
func TestSlowStatsJSONCarriesTheTextFigures(t *testing.T) {
	statsFixture(t)
	text, err := runRoot(t, "stats")
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, text)
	}
	raw, err := runRoot(t, "stats", "--json", "--since", "7d")
	if err != nil {
		t.Fatalf("stats --json: %v\n%s", err, raw)
	}
	var rep throughput.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	var again bytes.Buffer
	throughput.Text(&again, rep)
	if again.String() != text {
		t.Errorf("text from the JSON differs from the text output:\n--- json\n%s\n--- text\n%s", again.String(), text)
	}

	if len(rep.Repos) != 3 || rep.Repos[2].Name != "gamma" || rep.Repos[2].Error == "" {
		t.Fatalf("repos = %+v, want alpha, beta and an unreadable gamma", rep.Repos)
	}
	gamma := rep.Repos[2]
	if gamma.Stories.Value != nil || gamma.Stories.Reason == "" || gamma.Phase1.Median.Seconds != nil {
		t.Errorf("gamma figures = %+v / %+v, want null with a reason", gamma.Stories, gamma.Phase1.Median)
	}
	if !strings.Contains(raw, `"value": null`) || !strings.Contains(raw, `"seconds": null`) {
		t.Errorf("unavailable figures must encode as null:\n%s", raw)
	}
	alpha := rep.Repos[0]
	if !alpha.Phase2.Median.NotApplicable || alpha.Phase2.Median.Seconds != nil {
		t.Errorf("alpha phase 2 = %+v, want not applicable", alpha.Phase2.Median)
	}
	if rep.Total.Stories.Value == nil || *rep.Total.Stories.Value != 3 || len(rep.Total.Excluded) != 1 {
		t.Errorf("total = %+v, want 3 stories with gamma excluded", rep.Total)
	}
}

// Cost and estimate figures cover every registered repo, the total sums the
// readable ones with each item labelled by repo, an item finished before the
// window is absent, and the text carries the figures --json does.
func TestSlowStatsCostAndEstimatesAcrossRepos(t *testing.T) {
	statsFixture(t)
	raw, err := runRoot(t, "stats", "--json", "--since", "7d")
	if err != nil {
		t.Fatalf("stats --json: %v\n%s", err, raw)
	}
	var rep throughput.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	alpha, beta, gamma := rep.Repos[0], rep.Repos[1], rep.Repos[2]

	for _, sec := range []throughput.RepoReport{alpha, rep.Total} {
		s, ok := costItem(t, sec, statsCost.alphaIn)
		if !ok {
			t.Fatalf("%s cost has no item %s: %+v", sec.Name, statsCost.alphaIn, sec.Cost.Stories)
		}
		if !countIs(s.FreshInput, 1_000_000) || !countIs(s.CacheRead, 0) || !countIs(s.CacheWrite, 0) || !countIs(s.Output, 100_000) || !usdIs(s.Cost, 4.5) {
			t.Errorf("%s item %s usage = %+v, want 1000000 / 0 / 0 / 100000 at $4.50", sec.Name, s.ID, s.Usage)
		}
		if len(s.Estimates) != 1 || s.Estimates[0].Ratio == nil || *s.Estimates[0].Ratio != 2 {
			t.Errorf("%s item %s estimates = %+v, want fresh-input at 2.0", sec.Name, s.ID, s.Estimates)
		}
		if _, old := costItem(t, sec, statsCost.alphaOld); old {
			t.Errorf("%s cost carries %s, which finished before the window", sec.Name, statsCost.alphaOld)
		}
	}
	if s, ok := costItem(t, rep.Total, statsCost.alphaIn); !ok || s.Repo != "alpha" {
		t.Errorf("total item %s repo = %q, want alpha", statsCost.alphaIn, s.Repo)
	}
	if s, ok := costItem(t, rep.Total, statsCost.beta); !ok || s.Repo != "beta" || !usdIs(s.Cost, 1.35) {
		t.Errorf("total item %s = %+v, want beta's at $1.35", statsCost.beta, s)
	}
	if !usdIs(alpha.Cost.Cost, 4.5) || !usdIs(beta.Cost.Cost, 1.35) || !usdIs(rep.Total.Cost.Cost, 5.85) {
		t.Errorf("repo costs = %+v / %+v / %+v, want $4.50, $1.35 and a $5.85 total", alpha.Cost.Cost, beta.Cost.Cost, rep.Total.Cost.Cost)
	}
	if len(rep.Total.Cost.BySeat) != 2 || rep.Total.Cost.BySeat[0].Name != "coder" || !usdIs(rep.Total.Cost.BySeat[1].Cost, 1.35) {
		t.Errorf("total by seat = %+v, want coder $4.50 then reviewer $1.35", rep.Total.Cost.BySeat)
	}
	if len(rep.Total.Cost.ByModel) != 1 || rep.Total.Cost.ByModel[0].Name != "m-big" || !usdIs(rep.Total.Cost.ByModel[0].Cost, 5.85) {
		t.Errorf("total by model = %+v, want m-big at $5.85", rep.Total.Cost.ByModel)
	}

	// Four items finished across the readable repos; one ratio per unit.
	for unit, sp := range map[string]throughput.Spread{"fresh-input": spreadOf(rep.Total, "fresh-input"), "output": spreadOf(rep.Total, "output")} {
		if !countIs(sp.Stories, 1) || !countIs(sp.Unavailable, 3) || sp.Median.Value == nil {
			t.Errorf("total %s spread = %+v, want 1 item and 3 unavailable", unit, sp)
		}
	}
	if gamma.Cost.Cost.USD != nil || gamma.Cost.FreshInput.Value != nil || spreadOf(gamma, "output").Stories.Value != nil {
		t.Errorf("unreadable gamma cost = %+v, want unavailable", gamma.Cost)
	}

	text, err := runRoot(t, "stats", "--since", "7d")
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, text)
	}
	for _, want := range []string{
		fmt.Sprintf("item %s \"story 0\" (repo alpha): fresh input 1000000  cache read 0  cache write 0  output 100000  cost $4.5000", statsCost.alphaIn),
		"estimate output       est 100000  actual 50000  actual÷estimate 0.50",
		"by seat reviewer     fresh input 200000",
		"by model m-big       fresh input 1200000  cache read 0  cache write 0  output 150000  cost $5.8500",
		"fresh-input  items 1  unavailable 3  median 2.00  p90 2.00  min 2.00  max 2.00",
		"cost (each finished item's own rows): fresh input unavailable (repo unreadable: ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}

	help, err := runRoot(t, "stats", "--help")
	if err != nil {
		t.Fatalf("stats --help: %v", err)
	}
	for _, want := range []string{"Cost:", "by seat (agent) and by", "Estimate:", "actual÷estimate", "spread"} {
		if !strings.Contains(help, want) {
			t.Errorf("stats --help does not name %q:\n%s", want, help)
		}
	}
}

// The quality section is part of each repo's report and the total: beta's gate
// rejected its story once, and a story alpha created after beta's story was done
// cites it, so it came back in the total only. A cited story that was cancelled
// never came back.
func TestSlowStatsQualitySection(t *testing.T) {
	alpha, beta, _ := statsFixture(t)
	ctx := context.Background()

	bdb, err := store.Open(config.Config{}.ResolveDB(beta))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := bdb.Stories.List(ctx, workitem.ListFilter{Kind: workitem.KindStory})
	if err != nil || len(listed) != 1 {
		t.Fatalf("beta stories = %v, %v; want one", listed, err)
	}
	betaID := listed[0].ID
	statsAppend(t, bdb, betaID, ledger.KindReviewReject, ago(24*3), map[string]any{"from": "backlog", "to": "in_progress", "attempt": "att_1", "skill": "satelle-story-intent-review"})
	statsAppend(t, bdb, betaID, ledger.KindReviewAccept, ago(24*3-1), map[string]any{"from": "backlog", "to": "in_progress", "attempt": "att_2", "skill": "satelle-story-intent-review"})
	gone, err := bdb.Stories.Create(ctx, workitem.CreateInput{Kind: workitem.KindStory, Title: "dropped", Category: "feature"}, ago(24*3))
	if err != nil {
		t.Fatal(err)
	}
	statsAppend(t, bdb, gone.ID, ledger.KindStoryCreated, ago(24*3), map[string]any{"status": "backlog"})
	statsAppend(t, bdb, gone.ID, ledger.KindStatusTransition, ago(24*3-1), map[string]any{"from": "backlog", "to": "in_progress"})
	statsAppend(t, bdb, gone.ID, ledger.KindStatusTransition, ago(24*2), map[string]any{"from": "in_progress", "to": workitem.StatusCancelled})
	bdb.Close()

	adb, err := store.Open(config.Config{}.ResolveDB(alpha))
	if err != nil {
		t.Fatal(err)
	}
	redo, err := adb.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "redo", Category: "feature",
		Body: "fix what " + betaID + " shipped; " + gone.ID + " was dropped",
	}, ago(12))
	if err != nil {
		t.Fatal(err)
	}
	statsAppend(t, adb, redo.ID, ledger.KindStoryCreated, ago(12), map[string]any{"status": "backlog"})
	adb.Close()

	text, err := runRoot(t, "stats")
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, text)
	}
	sections := strings.Split(text, "\n== ")
	if len(sections) != 5 {
		t.Fatalf("want a window line, three repo sections and a total, got:\n%s", text)
	}
	for i, want := range [][]string{
		{"work that came back: 0\n"},
		{
			"in_progress                          0/1 (0%)  accepted: none  rejected: " + betaID,
			"satelle-story-intent-review          0/1 (0%)  accepted: none  rejected: " + betaID,
			"backlog → in_progress        mean 1.0  max 1  (1 stories)  at max: " + betaID,
			"coder re-dispatches after an exit rejection: 0  stories: none",
			"rejects naming no criterion: 1 of 1  stories: " + betaID,
		},
		{"first-pass acceptance by step:\n    unavailable (repo unreadable: "},
		{"work that came back: 1\n    " + redo.ID + " cites " + betaID + "\n"},
	} {
		for _, l := range want {
			if !strings.Contains(sections[i+1], l) {
				t.Errorf("section %q missing %q:\n%s", firstLine(sections[i+1]), l, sections[i+1])
			}
		}
	}

	raw, err := runRoot(t, "stats", "--json")
	if err != nil {
		t.Fatalf("stats --json: %v\n%s", err, raw)
	}
	var rep throughput.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	var again bytes.Buffer
	throughput.Text(&again, rep)
	if again.String() != text {
		t.Errorf("text from the JSON differs from the text output:\n--- json\n%s\n--- text\n%s", again.String(), text)
	}
	bq := rep.Repos[1].Quality
	if len(bq.FirstPassByStep.Rows) != 1 || fmt.Sprint(bq.FirstPassByStep.Rows[0].RejectedStories) != "["+betaID+"]" {
		t.Errorf("beta first pass = %+v, want %s rejected", bq.FirstPassByStep, betaID)
	}
	if fmt.Sprint(bq.NoCriterion.Stories) != "["+betaID+"]" {
		t.Errorf("beta no-criterion stories = %v", bq.NoCriterion.Stories)
	}
	cb := rep.Total.Quality.CameBack
	if cb.Count.Value == nil || *cb.Count.Value != 1 || len(cb.Pairs) != 1 || cb.Pairs[0] != (throughput.CameBackPair{Story: redo.ID, Cites: betaID}) {
		t.Errorf("total came back = %+v, want %s citing %s", cb, redo.ID, betaID)
	}
	if !strings.Contains(raw, `"quality"`) || !strings.Contains(raw, `"came_back"`) {
		t.Errorf("JSON missing the quality section:\n%s", raw)
	}
}
