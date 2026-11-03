package serve

import "testing"

// parseOutcome reads the closing JSON line; anything else is clean, so an older
// binary that prints only prose never makes satelled back off.
func TestParseOutcome(t *testing.T) {
	for _, c := range []struct {
		name string
		out  string
		want syncOutcome
		ok   bool
	}{
		{"clean", "Pushed 3 item(s).\n{\"held\":0,\"conflict\":0}\n", syncOutcome{}, true},
		{"held", "held sty_x: uncommitted changes\n{\"held\":1,\"conflict\":0}\n", syncOutcome{Held: 1}, true},
		{"conflict", "Snapshot work-state ...\n{\"held\":0,\"conflict\":2}", syncOutcome{Conflict: 2}, true},
		{"old binary prose", "Work-state up to date on https://x — no records changed.\n", syncOutcome{}, false},
		{"malformed", "{\"held\":", syncOutcome{}, false},
		{"empty", "", syncOutcome{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseOutcome([]byte(c.out))
			if got != c.want || ok != c.ok {
				t.Fatalf("parseOutcome(%q) = %+v, %v; want %+v, %v", c.out, got, ok, c.want, c.ok)
			}
		})
	}
}

// An older binary refuses --outcome-json; the helper runs the verb again
// without it and reads that run as clean.
func TestExecWithOutcomeFallsBackForAnOldBinary(t *testing.T) {
	f := newFakeCLI(t)
	bin, _ := resolveSatelle()
	t.Setenv("SATELLE_TEST_EXIT", "0")
	// The fake ignores flags, so give it an old binary's refusal by hand.
	old := `#!/bin/sh
echo "$*" >> "` + f.calls + `"
case "$*" in *--outcome-json*) echo "Error: unknown flag: --outcome-json" >&2; exit 1;; esac
exit 0
`
	writeScript(t, bin, old)
	o, err := execWithOutcome(t.Context(), bin, t.TempDir(), "sync", "workstate", "push")
	if err != nil || o != (syncOutcome{}) {
		t.Fatalf("old binary: outcome %+v err %v, want a clean run", o, err)
	}
	if got := f.lines(t); len(got) != 2 || got[1] != "sync workstate push" {
		t.Fatalf("runs = %q, want the flagged run then the bare one", got)
	}
}
