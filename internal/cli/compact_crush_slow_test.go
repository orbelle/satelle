//go:build clislow

package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/retrieve"
)

// TestSlowCompactCrushKeepsErrorAndRetrieves (sty_aa34491d AC3/AC4): over the
// size budget a marked command's list is crushed, the error row survives, and
// the trailing marker resolves through `satelle retrieve` to the full array.
func TestSlowCompactCrushKeepsErrorAndRetrieves(t *testing.T) {
	repo := tempRepo(t)
	appendOutputConfig(t, repo, fmt.Sprintf(crushTestTOML, 500))
	id := seedCrushLedger(t, 60, 31)

	plain, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "note", "--json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "note", "--compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(plain) {
		t.Fatalf("crushed output %d not smaller than plain %d", len(out), len(plain))
	}
	if !strings.Contains(out, "panic: nil pointer") {
		t.Errorf("error row dropped:\n%s", out)
	}
	hashes := retrieve.FindHashes(out)
	if len(hashes) != 1 || !strings.Contains(out, `"_crushed"`) {
		t.Fatalf("want one trailing crush marker, got hashes %v:\n%s", hashes, out)
	}
	got, err := runRoot(t, "retrieve", hashes[0])
	if err != nil {
		t.Fatal(err)
	}
	var want, have []map[string]any
	if err := json.Unmarshal([]byte(plain), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &have); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Errorf("retrieved array differs from original (%d vs %d rows)", len(have), len(want))
	}
}

// TestSlowCompactCrushScope (AC4): a list still under the size threshold after the
// lossless fold is not crushed.
func TestSlowCompactCrushScope(t *testing.T) {
	repo := tempRepo(t)
	appendOutputConfig(t, repo, fmt.Sprintf(crushTestTOML, 10_000_000))
	id := seedCrushLedger(t, 30, 7)
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "note", "--compact")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "_crushed") || !isCompactTable(out) {
		t.Errorf("under threshold must stay the lossless table:\n%.300s", out)
	}
}

// TestSlowCompactCrushSkipsUnmarkedVerb (AC4): a verb outside compact_commands is
// never crushed, whatever its size.
func TestSlowCompactCrushSkipsUnmarkedVerb(t *testing.T) {
	repo := tempRepo(t)
	appendOutputConfig(t, repo, strings.Replace(fmt.Sprintf(crushTestTOML, 1), `["ledger-list"]`, `["story-list"]`, 1))
	id := seedCrushLedger(t, 30, 7)
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "note", "--compact")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "_crushed") {
		t.Errorf("unmarked verb crushed:\n%.300s", out)
	}
}
