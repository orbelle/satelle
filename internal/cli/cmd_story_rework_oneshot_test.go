package cli

import (
	"strings"
	"testing"
)

// sty_47525ee9 AC2/AC3: `story rework` refuses a one-shot (interface=command)
// coder by naming the command that re-dispatches it, filled in for the story,
// and that command is no dead end: with no gate reject, only the orchestrator's
// directed message waiting, running it dispatches the coder once with that
// message in its payload, and the story is neither moved nor parked.
func TestStoryReworkOneShotRefusalNamesACommandThatRedispatches(t *testing.T) {
	dir, id := redispatchRepo(t)
	if n := len(coderRuns(t, dir)); n != 1 {
		t.Fatalf("the coder runs once on entry, ran %d times", n)
	}
	if out, err := runRoot(t, "story", "message", id, "--from", "orchestrator", "--to", "coder",
		"--body", "ORCH-ONLY-MARKER: the guard allows a downgrade"); err != nil {
		t.Fatalf("message: %v\n%s", err, out)
	}

	out, err := runRoot(t, "story", "rework", id)
	if err == nil {
		t.Fatalf("a command-interface coder cannot be relayed:\n%s", out)
	}
	msg := err.Error()
	want := "satelle story set " + id + " --status in_progress"
	for _, w := range []string{want, "unanswered messages"} {
		if !strings.Contains(msg, w) {
			t.Errorf("refusal must name %q, got: %v", w, msg)
		}
	}
	_, named, ok := strings.Cut(msg, "by: ")
	if !ok || strings.TrimSpace(named) != want {
		t.Fatalf("refusal must end by naming exactly %q, got: %v", want, msg)
	}

	// Run exactly the named command.
	args := strings.Fields(strings.TrimPrefix(strings.TrimSpace(named), "satelle "))
	if out, err := runRoot(t, args...); err != nil {
		t.Fatalf("the named command: %v\n%s", err, out)
	}
	runs := coderRuns(t, dir)
	if len(runs) != 2 {
		t.Fatalf("the named command must dispatch the coder exactly once more, ran %d times", len(runs))
	}
	if !strings.Contains(runs[1], "ORCH-ONLY-MARKER") {
		t.Errorf("the re-dispatched payload must carry the directed message:\n%s", runs[1])
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Fatalf("status = %q, the re-dispatch neither moves nor parks the story", got)
	}
}
