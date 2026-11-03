//go:build clislow

package cli

import (
	"encoding/json"
	"testing"
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
