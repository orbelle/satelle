package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/throughput"
	"github.com/bobmcallan/satelle/internal/workitem"
)

var statsClock = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// statsPRStep overlays the shipped [coded] step (in_progress) with pr_step, so a
// story on this repo's route has a second phase.
const statsPRStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
description = "fixture: the coded step raises the pull request."

[coded]
status = "in_progress"
agent = "executor"
reviewers = ["satelle-story-intent-review"]
reviewer_agent = "reviewer"
requires = ["raised"]
pr_step = true
`

// statsStory is one seeded story: its category, when it was created (in hours
// before statsClock), and the statuses it moved through, each with its hour.
type statsStory struct {
	category string
	created  float64
	moves    []statsMove
}

type statsMove struct {
	ago      float64
	from, to string
}

func ago(h float64) time.Time { return statsClock.Add(-time.Duration(h * float64(time.Hour))) }

// seedStatsRepo writes a repo at root holding stories, each with its
// story_created and status_transition rows at the given times, and returns
// their ids in order. stepToml, when set, is the repo's own step catalogue.
func seedStatsRepo(t *testing.T, root, stepToml string, stories ...statsStory) []string {
	t.Helper()
	dataDir := filepath.Join(root, config.DefaultDataDir)
	if err := os.MkdirAll(filepath.Join(dataDir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.ConfigName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if stepToml != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "workflows", "step.toml"), []byte(stepToml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(config.Config{}.ResolveDB(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var ids []string
	for i, s := range stories {
		it, err := db.Stories.Create(ctx, workitem.CreateInput{
			Kind: workitem.KindStory, Title: fmt.Sprintf("story %d", i), Category: s.category,
		}, ago(s.created))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
			StoryID: it.ID, Kind: ledger.KindStoryCreated, Payload: json.RawMessage(`{"status":"backlog"}`),
		}, ago(s.created)); err != nil {
			t.Fatal(err)
		}
		for _, m := range s.moves {
			p, _ := json.Marshal(map[string]string{"from": m.from, "to": m.to})
			if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
				StoryID: it.ID, Kind: ledger.KindStatusTransition, Body: m.from + " → " + m.to, Payload: p,
			}, ago(m.ago)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return ids
}

// seedStatsCost tags the story id in the repo at root and appends one measured
// dispatch row per usage payload.
func seedStatsCost(t *testing.T, root, id string, tags []string, usage ...map[string]any) {
	t.Helper()
	db, err := store.Open(config.Config{}.ResolveDB(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Stories.Update(ctx, id, workitem.UpdateInput{Tags: &tags}, statsClock.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, u := range usage {
		p := map[string]any{"adapter": "claude", "usage_available": true}
		for k, v := range u {
			p[k] = v
		}
		raw, _ := json.Marshal(p)
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: id, Kind: ledger.KindAgentInvocation, Payload: raw}, statsClock.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
}

func statsLedgerCount(t *testing.T, root string) int {
	t.Helper()
	db, err := store.OpenReadOnly(config.Config{}.ResolveDB(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := db.Ledger.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// statsCostIDs are the stories statsFixture gives usage rows: alpha's story
// finished in the window, alpha's story finished before it, and beta's.
type statsCostIDs struct{ alphaIn, alphaOld, beta string }

var statsCost statsCostIDs

// statsFixture registers three repos: alpha on the shipped route (no PR step),
// beta whose own route marks a PR step, and gamma, which is not a satelle repo.
// The operator's [pricing] rates m-big (priceRatesToml).
func statsFixture(t *testing.T) (alpha, beta, gamma string) {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	base := t.TempDir()
	alpha, beta, gamma = filepath.Join(base, "alpha"), filepath.Join(base, "beta"), filepath.Join(base, "gamma")
	if err := os.MkdirAll(gamma, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("SATELLE_HOME"), config.GlobalConfigName), []byte(priceRatesToml), 0o644); err != nil {
		t.Fatal(err)
	}
	days := func(d float64) float64 { return d * 24 }
	alphaIDs := seedStatsRepo(t, alpha, "",
		// Created before the window, finished inside it.
		statsStory{"feature", days(10), []statsMove{{days(2), "backlog", "in_progress"}, {days(1), "in_progress", "done"}}},
		statsStory{"epic-parent", days(3), []statsMove{{days(3) - 1, "backlog", "ready"}, {days(2), "ready", "merging"}, {days(2) - 1, "merging", "done"}}},
		// A parent container is a story, not an epic.
		statsStory{"parent", days(4), []statsMove{{days(4) - 1, "backlog", "ready"}, {days(3), "ready", "merging"}, {days(3) - 1, "merging", "done"}}},
		// Still open.
		statsStory{"feature", days(2), []statsMove{{days(1), "backlog", "in_progress"}}},
		// Finished before the window.
		statsStory{"feature", days(20), []statsMove{{days(19), "backlog", "in_progress"}, {days(18), "in_progress", "done"}}},
	)
	betaIDs := seedStatsRepo(t, beta, statsPRStep,
		statsStory{"feature", days(5), []statsMove{{days(4), "backlog", "in_progress"}, {days(1), "in_progress", "done"}}},
	)
	statsCost = statsCostIDs{alphaIn: alphaIDs[0], alphaOld: alphaIDs[4], beta: betaIDs[0]}
	// $3 + $1.50; actual fresh input is 2x its estimate.
	seedStatsCost(t, alpha, statsCost.alphaIn, []string{"estimate-fresh-input:500000"},
		map[string]any{"agent": "coder", "model_resolved": "m-big", "tokens_in_fresh": 1_000_000, "tokens_in": 1_000_000, "tokens_out": 100_000})
	seedStatsCost(t, alpha, statsCost.alphaOld, nil,
		map[string]any{"agent": "coder", "model_resolved": "m-big", "tokens_in_fresh": 7, "tokens_in": 7, "tokens_out": 7})
	// $0.60 + $0.75; actual output is half its estimate.
	seedStatsCost(t, beta, statsCost.beta, []string{"estimate-output:100000"},
		map[string]any{"agent": "reviewer", "model_resolved": "m-big", "tokens_in_fresh": 200_000, "tokens_in": 200_000, "tokens_out": 50_000})
	prevReg, prevNow := statsRegistry, statsNow
	statsRegistry = func() ([]string, error) { return []string{alpha, beta, gamma, alpha}, nil }
	statsNow = func() time.Time { return statsClock }
	t.Cleanup(func() { statsRegistry, statsNow = prevReg, prevNow })
	return alpha, beta, gamma
}

func TestStatsReportsPerRepoAndTotal(t *testing.T) {
	alpha, beta, _ := statsFixture(t)
	before := statsLedgerCount(t, alpha) + statsLedgerCount(t, beta)

	text, err := runRoot(t, "stats")
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, text)
	}
	sections := strings.Split(text, "\n== ")
	if len(sections) != 5 {
		t.Fatalf("want a window line, three repo sections and a total, got:\n%s", text)
	}
	want := []struct {
		head  string
		lines []string
	}{
		{"repo alpha", []string{
			"finished: stories 2  epics 1",
			"2026-10-28  stories 1  epics 0",
			"2026-10-27  stories 0  epics 1",
			"2026-10-23  stories 0  epics 0",
			"phase 1 (start to PR step, or to terminal): median 25h0m0s  p90 216h0m0s  (3 items)",
			"phase 2 (PR step to terminal): median n/a  p90 n/a  (0 items)",
			"in_progress      24h0m0s  (1 items)",
		}},
		{"repo beta", []string{
			"finished: stories 1  epics 0",
			"phase 1 (start to PR step, or to terminal): median 24h0m0s  p90 24h0m0s  (1 items)",
			"phase 2 (PR step to terminal): median 72h0m0s  p90 72h0m0s  (1 items)",
		}},
		{"repo gamma", []string{
			"error: ",
			"finished: stories unavailable (repo unreadable: ",
			"phase 1 (start to PR step, or to terminal): median unavailable (repo unreadable: ",
		}},
		{"total (excluded, unreadable: gamma)", []string{
			"finished: stories 3  epics 1",
			"phase 2 (PR step to terminal): median 72h0m0s  p90 72h0m0s  (1 items)",
		}},
	}
	for i, w := range want {
		sec := sections[i+1]
		if !strings.HasPrefix(sec, w.head) {
			t.Errorf("section %d = %q, want it to open %q", i+1, firstLine(sec), w.head)
			continue
		}
		for _, l := range w.lines {
			if !strings.Contains(sec, l) {
				t.Errorf("%s section missing %q:\n%s", w.head, l, sec)
			}
		}
	}

	if after := statsLedgerCount(t, alpha) + statsLedgerCount(t, beta); after != before {
		t.Errorf("ledger rows %d before, %d after: stats must write nothing", before, after)
	}
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func costItem(t *testing.T, r throughput.RepoReport, id string) (throughput.StoryCost, bool) {
	t.Helper()
	for _, s := range r.Cost.Stories {
		if s.ID == id {
			return s, true
		}
	}
	return throughput.StoryCost{}, false
}

func spreadOf(r throughput.RepoReport, unit string) throughput.Spread {
	for _, s := range r.Spread {
		if s.Unit == unit {
			return s
		}
	}
	return throughput.Spread{}
}

func countIs(c throughput.Count, n int) bool { return c.Value != nil && *c.Value == n }

func usdIs(c throughput.Cost, usd float64) bool {
	return c.USD != nil && math.Abs(*c.USD-usd) < 1e-9
}

func TestStatsRepoFlagAndSince(t *testing.T) {
	_, beta, _ := statsFixture(t)
	prev := statsFind
	statsFind = func(name string) ([]string, error) {
		if name == "beta" {
			return []string{beta}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { statsFind = prev })

	out, err := runRoot(t, "stats", "--repo", "beta", "--since", "2d")
	if err != nil {
		t.Fatalf("stats --repo beta: %v\n%s", err, out)
	}
	if strings.Contains(out, "repo alpha") || !strings.Contains(out, "== repo beta") ||
		!strings.Contains(out, "window 2026-10-27T12:00:00Z to 2026-10-29T12:00:00Z") {
		t.Errorf("want beta alone over two days:\n%s", out)
	}
	if _, err := runRoot(t, "stats", "--repo", "nope"); err == nil || !strings.Contains(err.Error(), "not in the workspace registry") {
		t.Errorf("an unregistered --repo must be refused, got %v", err)
	}
	for _, bad := range []string{"soon", "0d", "-1h"} {
		if _, err := runRoot(t, "stats", "--since", bad); err == nil {
			t.Errorf("--since %q must be refused", bad)
		}
	}
}

// statsAppend writes one ledger row for story id in the repo at root.
func statsAppend(t *testing.T, db *store.DB, id, kind string, at time.Time, payload map[string]any) {
	t.Helper()
	p, _ := json.Marshal(payload)
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: id, Kind: kind, Payload: p}, at); err != nil {
		t.Fatal(err)
	}
}
