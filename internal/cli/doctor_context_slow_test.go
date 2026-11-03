//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// Doctor reports no context.dispatch.* seat finding: a seat's context is its
// CLI's to manage, so there is nothing to size it against — not even when the
// seat is large.
func TestSlowDoctorReportsNoSeatBudgetFindings(t *testing.T) {
	f := newCtxFixture(t, "")
	f.write("workflows/agents.toml", f.claudeAgents())
	f.principle("satelle-fixture-big", "# Big\n\n"+strings.Repeat("A long resident rule that is never trimmed. ", 4000), true)
	for _, fd := range checksFor(f) {
		if strings.HasPrefix(fd.ID, "context.dispatch.") {
			t.Errorf("doctor emitted a seat-sizing finding: %+v", fd)
		}
	}
}
