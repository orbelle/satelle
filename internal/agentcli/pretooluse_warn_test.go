package agentcli

import (
	"strings"
	"testing"
)

func TestPreToolUseWarnCellsRendered(t *testing.T) {
	table := HarnessFactsTableMarkdown()
	for _, f := range HarnessFactsTable() {
		if !strings.Contains(table, f.PreToolUseWarn.Cell()) {
			t.Errorf("%s: rendered facts table lacks the cell %q", f.Harness, f.PreToolUseWarn.Cell())
		}
	}
}
