package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestStorySetDoneReleasesEverySeat(t *testing.T) {
	newDoctorFixture(t)
	sessions := []string{"s-one", "s-two"}
	seated := func() string {
		id := createID(t, "story", "create", "--title", "finished", "--criteria", "c")
		for _, s := range sessions {
			must(t, "story", "seat", id, "--session", s)
		}
		return id
	}

	id := seated()
	out := must(t, "story", "set", id, "--status", "done")
	for _, s := range sessions {
		if !strings.Contains(out, id+": released the seat of session "+s) {
			t.Errorf("output does not report the release of %s:\n%s", s, out)
		}
	}
	if left := seatsOn(t, id); len(left) != 0 {
		t.Errorf("seats still bound after done: %+v", left)
	}

	id = seated()
	raw := must(t, "story", "set", id, "--status", "done", "--json")
	var res kernel.TransitionResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if !res.Moved || !slices.Equal(res.Released, sessions) {
		t.Errorf("json Moved=%v Released=%v, want both sessions released", res.Moved, res.Released)
	}
	if left := seatsOn(t, id); len(left) != 0 {
		t.Errorf("seats still bound after done --json: %+v", left)
	}
}
