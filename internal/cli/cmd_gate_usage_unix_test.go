//go:build !windows

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func usageRows(t *testing.T, out string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	header := false
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 0 || f[0] == "window" {
			continue
		}
		if f[0] == "HARNESS" {
			header = true
			if strings.Join(f, " ") != "HARNESS MODEL GATE RUNS ACCEPT REJECT ERROR FALLBACK DURATION" {
				t.Fatalf("header %q", ln)
			}
			continue
		}
		if len(f) != 9 {
			t.Fatalf("row %q has %d cells, want 9", ln, len(f))
		}
		rows[strings.Join(f[:3], " ")] = strings.Join(f[3:], " ")
	}
	if !header {
		t.Fatalf("no header in:\n%s", out)
	}
	return rows
}

func usageItem(t *testing.T) string {
	t.Helper()
	isolate(t)
	must(t, "init")
	return createID(t, "story", "create", "--title", "usage")
}

func TestGateUsagePrintsPerHarnessModelAndGate(t *testing.T) {
	id := usageItem(t)
	seedGateRun(t, id, `{"transition":"a->b","gate":"code","binding":"agy-rev","command":"agy","model":"gemini-x","result":"accept","duration_ms":3000}`)
	seedGateRun(t, id, `{"transition":"a->b","gate":"code","binding":"agy-rev","command":"agy","model":"gemini-x","result":"reject","duration_ms":5000}`)
	seedGateRun(t, id, `{"transition":"a->b","gate":"code","binding":"cur-rev","command":"cursor-agent","model":"gemini-x","result":"accept","duration_ms":4000,"fallback_from":{"binding":"agy-rev","command":"agy"}}`)
	seedGateRun(t, id, `{"transition":"a->b","gate":"fmt","result":"accept","duration_ms":10}`)
	out := must(t, "gate", "usage")
	if !strings.HasPrefix(out, "window ") {
		t.Errorf("output does not open with the window line:\n%s", out)
	}
	rows := usageRows(t, out)
	want := map[string]string{
		"agy gemini-x code":          "2 1 1 0 0 8s",
		"cursor-agent gemini-x code": "1 1 0 0 1 4s",
	}
	if len(rows) != len(want) {
		t.Errorf("rows %v, want %v", rows, want)
	}
	for k, w := range want {
		if rows[k] != w {
			t.Errorf("%s = %q, want %q", k, rows[k], w)
		}
	}
	var doc struct {
		Since string            `json:"since"`
		Rows  []kernel.UsageRow `json:"rows"`
	}
	if err := json.Unmarshal([]byte(must(t, "gate", "usage", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	wantJSON := []kernel.UsageRow{
		{Harness: "agy", Model: "gemini-x", Gate: "code", Runs: 2, Accepts: 1, Rejects: 1, DurationMS: 8000},
		{Harness: "cursor-agent", Model: "gemini-x", Gate: "code", Runs: 1, Accepts: 1, Fallbacks: 1, DurationMS: 4000},
	}
	if doc.Since == "" || len(doc.Rows) != len(wantJSON) {
		t.Fatalf("json %+v, want since set and rows %+v", doc, wantJSON)
	}
	for i := range wantJSON {
		if doc.Rows[i] != wantJSON[i] {
			t.Errorf("json row %d = %+v, want %+v", i, doc.Rows[i], wantJSON[i])
		}
	}
}

func TestGateUsageSinceCountsOnlyTheWindow(t *testing.T) {
	id := usageItem(t)
	payload := `{"transition":"a->b","gate":"code","binding":"agy-rev","command":"agy","model":"gemini-x","result":"accept","duration_ms":1000}`
	seedGateRun(t, id, payload)
	time.Sleep(50 * time.Millisecond)
	cut := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	seedGateRun(t, id, payload)
	if got := usageRows(t, must(t, "gate", "usage"))["agy gemini-x code"]; got != "2 2 0 0 0 2s" {
		t.Errorf("default window = %q, want both runs", got)
	}
	if got := usageRows(t, must(t, "gate", "usage", "--since", cut.Format(time.RFC3339Nano)))["agy gemini-x code"]; got != "1 1 0 0 0 1s" {
		t.Errorf("--since %s = %q, want only the later run", cut.Format(time.RFC3339Nano), got)
	}
	time.Sleep(50 * time.Millisecond)
	if rows := usageRows(t, must(t, "gate", "usage", "--since", "1ms")); len(rows) != 0 {
		t.Errorf("--since 1ms rows %v, want none", rows)
	}
}
