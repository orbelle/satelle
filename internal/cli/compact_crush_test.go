package cli

import (
	"fmt"
	"strings"
	"testing"
)

const crushTestTOML = `
[output]
compact_commands = ["ledger-list"]
[output.crush]
enabled = true
min_items = 5
size_threshold_bytes = %d
max_kept = 10
first_fraction = 0.3
last_fraction = 0.15
variance_sigma = 2.0
structural_outlier_fraction = 0.2
rare_status_fraction = 0.2
status_fields = ["kind"]
error_keywords = ["error", "panic"]
`

// seedCrushLedger appends n note rows to a fresh story, row `bad` reporting a
// panic; returns the story id.
func seedCrushLedger(t *testing.T, n, bad int) string {
	t.Helper()
	id := createStory(t, "crush fixture")
	for i := 0; i < n; i++ {
		body := "routine entry"
		if i == bad {
			body = "panic: nil pointer"
		}
		if out, err := runRoot(t, "ledger", "append", "--story", id, "--kind", "note", "--body", body); err != nil {
			t.Fatalf("ledger append: %v\n%s", err, out)
		}
	}
	return id
}

// TestCompactCrushMinItems (AC4): a list below min_items is never crushed even
// when over the size threshold.
func TestCompactCrushMinItems(t *testing.T) {
	repo := tempRepo(t)
	appendOutputConfig(t, repo, fmt.Sprintf(crushTestTOML, 1))
	id := seedCrushLedger(t, 4, 2)
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "note", "--compact")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "_crushed") {
		t.Errorf("4-row list crushed with min_items 5:\n%s", out)
	}
}
