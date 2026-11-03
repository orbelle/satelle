//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
)

// With every child terminal nothing is runnable or held, and a script still
// finds both keys as empty arrays.
func TestSlowStoryWaveJSONEmptyArraysArePresent(t *testing.T) {
	epic, free, waiting, dep := waveRepo(t, "parallel")
	for _, id := range append(append([]string{}, free...), dep, waiting) {
		for _, st := range []string{"in_progress", "done"} {
			if out, err := runRoot(t, "story", "set", id, "--status", st); err != nil {
				t.Fatalf("%s -> %s: %v\n%s", id, st, err, out)
			}
		}
	}
	out, stderr, err := runRootSplit(t, "", "story", "wave", epic, "--json")
	if err != nil {
		t.Fatalf("story wave --json: %v\nstderr: %s", err, stderr)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	for _, k := range []string{"runnable", "omitted"} {
		if string(raw[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, raw[k])
		}
	}
}

// The JSON and text forms render one response: the runnable ids agree, and the
// held child carries a machine-readable cause and its blocker.
func TestSlowStoryWaveJSONMatchesText(t *testing.T) {
	epic, _, waiting, dep := waveRepo(t, "parallel")
	text, stderr, err := runRootSplit(t, "", "story", "wave", epic)
	if err != nil {
		t.Fatalf("story wave: %v\nstderr: %s", err, stderr)
	}
	textIDs := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			textIDs[l] = true
		}
	}
	out, stderr, err := runRootSplit(t, "", "story", "wave", epic, "--json")
	if err != nil {
		t.Fatalf("story wave --json: %v\nstderr: %s", err, stderr)
	}
	var res verb.WaveResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json must print one JSON object: %v\n%s", err, out)
	}
	jsonIDs := map[string]bool{}
	for _, id := range res.Runnable {
		jsonIDs[id] = true
	}
	if len(jsonIDs) != len(textIDs) {
		t.Errorf("runnable = %v, text printed %v", res.Runnable, textIDs)
	}
	for id := range textIDs {
		if !jsonIDs[id] {
			t.Errorf("%s printed as runnable by text but not in JSON runnable %v", id, res.Runnable)
		}
	}
	if len(res.Omitted) != 1 || res.Omitted[0].ID != waiting {
		t.Fatalf("omitted = %+v, want only %s", res.Omitted, waiting)
	}
	o := res.Omitted[0]
	if o.Cause != verb.WaveCauseDependency {
		t.Errorf("cause = %q, want %q", o.Cause, verb.WaveCauseDependency)
	}
	if len(o.WaitingOn) != 1 || o.WaitingOn[0].ID != dep || o.WaitingOn[0].Status != "backlog" || o.WaitingOn[0].Cancelled {
		t.Errorf("waiting_on = %+v, want %s in backlog, not cancelled", o.WaitingOn, dep)
	}
}
