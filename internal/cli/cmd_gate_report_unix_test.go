//go:build !windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func seedPerformerRun(t *testing.T, id, payload string) {
	t.Helper()
	must(t, "ledger", "append", id, "--kind", "performer.run", "--payload", payload)
}

func fakePerformer(t *testing.T, text string) string {
	t.Helper()
	result, _ := json.Marshal(map[string]any{"result": text, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + string(result) + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func fakeToggleJudge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	flag := filepath.Join(dir, "seen")
	rejAns, _ := json.Marshal(map[string]string{"verdict": "reject", "notes": "round 1 reject"})
	accAns, _ := json.Marshal(map[string]string{"verdict": "accept", "notes": "round 2 accept"})
	rejOut, _ := json.Marshal(map[string]any{"result": "judged\n" + string(rejAns), "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	accOut, _ := json.Marshal(map[string]any{"result": "judged\n" + string(accAns), "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\nif [ -f '" + flag + "' ]; then\nprintf '%s\\n' '" + string(accOut) + "'\nelse\ntouch '" + flag + "'\nprintf '%s\\n' '" + string(rejOut) + "'\nfi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func writeOutcomeWorkflow(t *testing.T, dir string) (perfCmd, flipCmd, p1Cmd, p2Cmd string) {
	t.Helper()
	agents, err := os.ReadFile(filepath.Join(dir, ".satelle", "agents.toml"))
	if err != nil {
		t.Fatal(err)
	}
	perfCmd = fakePerformer(t, "worked")
	flipCmd = fakeToggleJudge(t)
	p1Cmd = fakeJudge(t, "accept", "p1 accept")
	p2Cmd = fakeJudge(t, "accept", "p2 accept")
	files := map[string]string{
		".satelle/agents.toml": string(agents) + `
[agents.perf-a]
command = "` + perfCmd + `"
model = "m-perf-a"
tools = ["Read"]
[agents.judge-flip]
command = "` + flipCmd + `"
model = "m-rev"
tools = ["Read"]
[agents.judge-p1]
command = "` + p1Cmd + `"
model = "m-p1"
tools = ["Read"]
[agents.judge-p2]
command = "` + p2Cmd + `"
model = "m-p2"
tools = ["Read"]
`,
		".satelle/workflows/outcome.toml": `name = "outcome"
[states.st-a]
start = true
performer = { agent = "perf-a", skill = "out-perf" }
[states.st-b]
terminal = true
[gates.chk]
skill = "out-ok"
[gates.rev]
skill = "out-rev"
agent = "judge-flip"
[gates.pan]
skill = "out-rev"
panel = ["judge-p1", "judge-p2"]
[[transition]]
from = ["st-a"]
to = "st-b"
groups = [{ mode = "sequential", gates = ["chk", "rev", "pan"] }]
`,
		".satelle/skills/out-perf.md": "---\nname: out-perf\n---\nPerform it.\n",
		".satelle/skills/out-ok.md":   "---\ntype: check\n---\n```sh\necho ok\n```\n",
		".satelle/skills/out-rev.md":  "---\nname: out-rev\n---\nReview it.\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return perfCmd, flipCmd, p1Cmd, p2Cmd
}

func itemOutcomeTable(t *testing.T, out string) (string, [][]string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("too few lines in:\n%s", out)
	}
	header := lines[0]
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "KIND STEP TRANSITION ROUND RESULT BINDING COMMAND MODEL" {
		t.Fatalf("table header %q", lines[1])
	}
	var rows [][]string
	for _, ln := range lines[2:] {
		f := strings.Fields(ln)
		if len(f) == 0 {
			continue
		}
		if len(f) != 8 {
			t.Fatalf("row %q has %d cells, want 8", ln, len(f))
		}
		rows = append(rows, f)
	}
	return header, rows
}

func TestGateReportItemOutcome(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	perfCmd, flipCmd, p1Cmd, p2Cmd := writeOutcomeWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "outcome", "--workflow", "outcome")
	must(t, "story", "perform", id)
	seedPerformerRun(t, id, `{"state":"st-mid","binding":"perf-mid","skill":"s","command":"codex","model":"m-perf-mid"}`)
	if _, err := run(t, "", "story", "set", id, "--status", "st-b"); err == nil {
		t.Fatal("round 1 st-a -> st-b passed; rev should reject round 1")
	}
	must(t, "story", "set", id, "--status", "st-b")

	before := must(t, "ledger", "list", id)
	out := must(t, "gate", "report", "--item", id)
	if after := must(t, "ledger", "list", id); after != before {
		t.Fatalf("gate report --item wrote to the ledger:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	header, rows := itemOutcomeTable(t, out)
	if want := id + "  workflow outcome  status st-b (terminal)"; header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
	want := [][]string{
		{"performer", "st-a", "-", "1", "ok", "perf-a", perfCmd, "m-perf-a"},
		{"performer", "st-mid", "-", "1", "ok", "perf-mid", "codex", "m-perf-mid"},
		{"gate", "rev", "st-a->st-b", "1", "reject", "judge-flip", flipCmd, "m-rev"},
		{"gate", "rev", "st-a->st-b", "2", "accept", "judge-flip", flipCmd, "m-rev"},
		{"gate", "pan", "st-a->st-b", "1", "accept", "judge-p1", p1Cmd, "m-p1"},
		{"gate", "pan", "st-a->st-b", "1", "accept", "judge-p2", p2Cmd, "m-p2"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows:\n%s", len(rows), out)
	}
	for i := range want {
		if strings.Join(rows[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
	for _, absent := range []string{"chk", "judge-p1,judge-p2"} {
		if strings.Contains(out, absent) {
			t.Errorf("output unexpectedly contains %q:\n%s", absent, out)
		}
	}
}

func TestGateReportItemNeverGuessesAModel(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOutcomeWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "model-less", "--workflow", "outcome")
	seedPerformerRun(t, id, `{"state":"st-a","binding":"perf-a","skill":"out-perf"}`)
	seedGateRun(t, id, `{"transition":"st-a->st-b","gate":"rev","binding":"judge-flip","result":"accept"}`)
	seedGateRun(t, id, `{"transition":"st-a->st-b","gate":"pan","binding":"judge-p1,judge-p2","result":"accept","members":[{"binding":"judge-p1","result":"accept"},{"binding":"judge-p2","model":"recorded-p2","result":"accept"}]}`)

	out := must(t, "gate", "report", "--item", id)
	_, rows := itemOutcomeTable(t, out)
	if len(rows) != 4 {
		t.Fatalf("got %d rows:\n%s", len(rows), out)
	}
	if rows[0][0] != "performer" || rows[0][6] != "-" || rows[0][7] != kernel.UnknownModel {
		t.Errorf("performer row = %v, want command - and model unknown", rows[0])
	}
	if rows[1][0] != "gate" || rows[1][1] != "rev" || rows[1][7] != kernel.UnknownModel {
		t.Errorf("single judge row = %v, want model unknown", rows[1])
	}
	if rows[2][0] != "gate" || rows[2][5] != "judge-p1" || rows[2][7] != kernel.UnknownModel {
		t.Errorf("panel member 1 = %v, want model unknown", rows[2])
	}
	if rows[3][0] != "gate" || rows[3][5] != "judge-p2" || rows[3][7] != "recorded-p2" {
		t.Errorf("panel member 2 = %v, want model recorded-p2", rows[3])
	}
	for _, live := range []string{"m-perf-a", "m-rev", "m-p1", "m-p2"} {
		if strings.Contains(out, live) {
			t.Errorf("output leaked agents.toml model %q:\n%s", live, out)
		}
	}
}

func TestGateReportItemJSONMatchesTheTable(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOutcomeWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "json outcome", "--workflow", "outcome")
	must(t, "story", "perform", id)
	if _, err := run(t, "", "story", "set", id, "--status", "st-b"); err == nil {
		t.Fatal("round 1 st-a -> st-b passed")
	}
	must(t, "story", "set", id, "--status", "st-b")
	seedGateRun(t, id, `{"transition":"st-a->st-b","gate":"legacy","binding":"judge-flip","result":"accept"}`)

	header, table := itemOutcomeTable(t, must(t, "gate", "report", "--item", id))
	out := must(t, "gate", "report", "--item", id, "--json")
	var doc kernel.ItemOutcome
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("more than one JSON value:\n%s", out)
	}
	if doc.Item != id || doc.Workflow != "outcome" || doc.Status != "st-b" || !doc.Terminal {
		t.Fatalf("doc header = %+v", doc)
	}
	if want := id + "  workflow outcome  status st-b (terminal)"; header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
	if len(doc.Rows) != len(table) {
		t.Fatalf("json rows %d, table rows %d", len(doc.Rows), len(table))
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for i, r := range doc.Rows {
		want := []string{r.Kind, r.Step, dash(r.Transition), strconv.Itoa(r.Round), r.Result, dash(r.Binding), dash(r.Command), r.Model}
		if strings.Join(want, " ") != strings.Join(table[i], " ") {
			t.Errorf("row %d: json %v, table %v", i, want, table[i])
		}
	}

	emptyID := createID(t, "story", "create", "--title", "fresh", "--workflow", "outcome")
	emptyJSON := must(t, "gate", "report", "--item", emptyID, "--json")
	if !strings.Contains(emptyJSON, `"rows": []`) {
		t.Errorf("empty item JSON lacks empty array:\n%s", emptyJSON)
	}
	var emptyDoc kernel.ItemOutcome
	if err := json.Unmarshal([]byte(emptyJSON), &emptyDoc); err != nil {
		t.Fatal(err)
	}
	if emptyDoc.Terminal || len(emptyDoc.Rows) != 0 {
		t.Errorf("empty item doc = %+v", emptyDoc)
	}
	emptyHeader, emptyRows := itemOutcomeTable(t, must(t, "gate", "report", "--item", emptyID))
	if want := emptyID + "  workflow outcome  status st-a"; emptyHeader != want || len(emptyRows) != 0 {
		t.Errorf("empty item table = %q %v, want %q []", emptyHeader, emptyRows, want)
	}
}

func TestGateReportItemRejectsBadFlagsAndMissingItem(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "flags")
	for _, args := range [][]string{
		{"gate", "report", "--item", id, "--since", "1h"},
		{"gate", "report", "--item", id, "--until", "2026-12-01"},
	} {
		_, err := run(t, "", args...)
		if err == nil {
			t.Fatalf("%v passed", args)
		}
		for _, want := range []string{"--since", "--until", "--item"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v error %q does not name %q", args, err, want)
			}
		}
	}
	for _, bad := range []string{"nosuch_id", "sto_00000000"} {
		if _, err := run(t, "", "gate", "report", "--item", bad); err == nil {
			t.Errorf("--item %s passed", bad)
		}
	}
}
