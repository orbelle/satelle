package cli

import (
	"testing"
)

func TestReservedKeepFile(t *testing.T) {
	for _, fn := range []string{"README.md", "index.md", "log.md"} {
		if !reservedKeepFile(fn) {
			t.Errorf("%s should be a reserved keep-file (exempt)", fn)
		}
	}
	for _, fn := range []string{"satelle-story-review.md", "my-skill.md", "anything.md"} {
		if reservedKeepFile(fn) {
			t.Errorf("%s must NOT be exempt — it is an authored doc", fn)
		}
	}
}

const scheduleRouteDone = `["*"]
obligations = ["raised", "ready", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`

func scheduleRouteStep(scheduleLines string) string {
	return `[raised]
status = "backlog"
start = true

[ready]
status = "ready"
waits_on_children = true
` + scheduleLines + `requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["ready"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`
}
