//go:build !windows

package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func reportRows(t *testing.T, out string) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	header := false
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 0 || f[0] == "window" {
			continue
		}
		if f[0] == "GATE" {
			header = true
			if strings.Join(f, " ") != "GATE MODEL RUNS ACCEPT REJECT ERROR CATCH OVERTURNED MEASURED TOKENS-IN TOKENS-OUT MEDIAN" {
				t.Fatalf("header %q", ln)
			}
			continue
		}
		if len(f) != 12 {
			t.Fatalf("row %q has %d cells, want 12", ln, len(f))
		}
		rows[f[0]+"/"+f[1]] = f[2:]
	}
	if !header {
		t.Fatalf("no header in:\n%s", out)
	}
	return rows
}

func seedGateRun(t *testing.T, id, payload string) {
	t.Helper()
	must(t, "ledger", "append", id, "--kind", "gate.run", "--payload", payload)
}

func seededReport(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeDryWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "report")
	seedGateRun(t, id, `{"transition":"a->b","gate":"seeded","binding":"j","model":"seeded-m","result":"reject","coverage":"c1","tokens_in":40,"tokens_out":4,"duration_ms":3000}`)
	seedGateRun(t, id, `{"transition":"a->b","gate":"seeded","binding":"j","model":"seeded-m","result":"accept","coverage":"c2","tokens_in":60,"tokens_out":6,"duration_ms":5000}`)
	return id
}

func TestGateReportPrintsPerGateAndModel(t *testing.T) {
	seededReport(t)
	moved := createID(t, "story", "create", "--title", "judged", "--workflow", "dry")
	if _, err := run(t, "", "story", "set", moved, "--status", "st-b"); err == nil {
		t.Fatal("st-a -> st-b passed; the dry workflow's bad gate should refuse it")
	}
	rows := reportRows(t, must(t, "gate", "report"))
	if got := strings.Join(rows["seeded/seeded-m"], " "); got != "2 1 1 0 1 0 2 100 10 4s" {
		t.Errorf("seeded/seeded-m = %q, want 2 runs, 1 accept, 1 reject, 1 catch, 2 measured, 100/10 tokens, median 4s", got)
	}
	for key, want := range map[string]string{"one/fake-yes": "1 1 0 0", "split/fake-yes": "1 1 0 0", "split/fake-no": "1 0 1 0"} {
		if got := strings.Join(rows[key][:4], " "); got != want {
			t.Errorf("%s runs/accept/reject/error = %q, want %q (rows %v)", key, got, want, rows)
		}
	}
	for key := range rows {
		if strings.Contains(key, ",") || strings.HasPrefix(key, "ok/") || strings.HasPrefix(key, "bad/") {
			t.Errorf("row %s: a panel's joined binding or a check is not a judge row", key)
		}
	}
}

func TestGateReportJSONMatchesTheTable(t *testing.T) {
	seededReport(t)
	table := reportRows(t, must(t, "gate", "report"))
	var doc struct {
		Since *string           `json:"since"`
		Rows  []kernel.JudgeRow `json:"rows"`
	}
	out := must(t, "gate", "report", "--json")
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("more than one JSON value:\n%s", out)
	}
	if doc.Since != nil || len(doc.Rows) != len(table) {
		t.Fatalf("doc %+v, want %d rows and no since", doc, len(table))
	}
	for _, r := range doc.Rows {
		cells := table[r.Gate+"/"+r.Model]
		got := []int{r.Runs, r.Accepts, r.Rejects, r.Errors, r.ConfirmedCatches, r.OverturnedRejects, r.Measured, r.TokensIn, r.TokensOut}
		for i, n := range got {
			if strconv.Itoa(n) != cells[i] {
				t.Errorf("%s/%s cell %d: json %d, table %s", r.Gate, r.Model, i, n, cells[i])
			}
		}
		if r.Gate == "seeded" && r.MedianDurationMS != 4000 {
			t.Errorf("seeded median %d, want 4000", r.MedianDurationMS)
		}
	}
	if out := must(t, "gate", "report", "--until", "2000-01-01", "--json"); !strings.Contains(out, `"rows": []`) || !strings.Contains(out, `"until"`) {
		t.Errorf("empty window JSON:\n%s", out)
	}
}

func TestGateReportNeverGuessesAModel(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeDryWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "old run")
	seedGateRun(t, id, `{"transition":"a->b","gate":"old","binding":"judge-yes","result":"accept"}`)
	rows := reportRows(t, must(t, "gate", "report"))
	if got := rows["old/unknown"]; len(got) == 0 || got[0] != "1" {
		t.Errorf("old/unknown = %v, want one run (rows %v)", got, rows)
	}
	if _, ok := rows["old/fake-yes"]; ok {
		t.Errorf("the model was read from agents.toml: %v", rows)
	}
}

func TestGateReportWindowFlags(t *testing.T) {
	seededReport(t)
	if rows := reportRows(t, must(t, "gate", "report", "--since", "1h")); len(rows["seeded/seeded-m"]) == 0 {
		t.Errorf("--since 1h left out runs seeded now: %v", rows)
	}
	out := must(t, "gate", "report", "--until", "2000-01-01")
	if rows := reportRows(t, out); len(rows) != 0 || !strings.Contains(out, "window the first run to 2000-01-01") {
		t.Errorf("--until 2000-01-01:\n%s", out)
	}
	_, err := run(t, "", "gate", "report", "--since", "nonsense")
	if err == nil {
		t.Fatal("--since nonsense passed")
	}
	for _, want := range []string{"--since", "RFC 3339", "YYYY-MM-DD", "duration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"", time.Time{}},
		{"2026-11-19T08:30:00Z", time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC)},
		{"2026-11-19", time.Date(2026, 11, 19, 0, 0, 0, 0, time.Local)},
		{"36h", now.Add(-36 * time.Hour)},
	} {
		got, err := parseWhen(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	if _, err := parseWhen("yesterday", now); err == nil {
		t.Error("parseWhen(yesterday) passed")
	}
}
