package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// sty_0367d20c AC2: the sync children satelled runs stamp their ingests hosted,
// so their own writes never schedule another push. push and follow do so by
// annotation; `workspace add` — local when an operator runs it — only when
// satelled sets mirror.EnvIngestOrigin on the reseed child.
func TestSatelledSyncChildrenDrainAsHostedOrigin(t *testing.T) {
	t.Setenv(mirror.EnvIngestOrigin, "")
	root := NewRootCmd()
	origin := func(path ...string) mirror.IngestOrigin {
		t.Helper()
		cmd, _, err := root.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("no %v command: %v", path, err)
		}
		return ingestOrigin(cmd)
	}
	for _, path := range [][]string{{"sync", "workstate", "push"}, {"sync", "workstate", "follow"}} {
		if got := origin(path...); got != mirror.OriginHosted {
			t.Errorf("%v drains as %q, want hosted", path, got)
		}
	}
	if got := origin("workspace", "add"); got != mirror.OriginLocal {
		t.Errorf("an operator's workspace add drains as %q, want local", got)
	}
	t.Setenv(mirror.EnvIngestOrigin, string(mirror.OriginHosted))
	if got := origin("workspace", "add"); got != mirror.OriginHosted {
		t.Errorf("satelled's reseed workspace add drains as %q, want hosted", got)
	}
}

// outcomeLine finds the --outcome-json line in a verb's output.
func outcomeLine(out string) (workstateOutcome, bool) {
	var o workstateOutcome
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, `{"held":`) {
			return o, json.Unmarshal([]byte(ln), &o) == nil
		}
	}
	return o, false
}

func runRootOut(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := runRoot(t, args...)
	return out
}
