package verb_test

import (
	"strings"
)

// sty_2b737ed5: a container readiness step declares whether `story ready` also
// readies each child. With ready_children = false the epic's own round is the
// whole readiness and each child is planned when the wave lets it start.

// withReadyChildren declares ready_children = value on the fixture's container
// readiness step.
func withReadyChildren(stepToml, value string) string {
	return strings.Replace(stepToml, "waits_on_children = true\n", "waits_on_children = true\nready_children = "+value+"\n", 1)
}
