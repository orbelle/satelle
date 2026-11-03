//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// TestSlowLedgerListOmissionNotice pins sty_523edea7 AC1/AC2 at the CLI: a ledger
// longer than the default limit lists its newest rows on stdout (still a JSON
// array, newest last) and one stderr line says how many older rows were left
// out and how to see them; a limit that fits prints no notice.
func TestSlowLedgerListOmissionNotice(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "story", "create", "--title", "long ledger")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Seeded after create and stamped later, so these are the newest rows
	// whatever create wrote.
	base := time.Now().Add(time.Hour)
	for i := 1; i <= 250; i++ {
		if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
			StoryID: created.ID, Kind: "note", Body: fmt.Sprintf("row-%d", i),
		}, base.Add(time.Duration(i)*time.Second)); err != nil {
			db.Close()
			t.Fatalf("append: %v", err)
		}
	}
	db.Close()

	// The whole ledger in one read gives the true total, and no notice.
	all, stderr, err := runRootSplit(t, "", "ledger", "list", "--story", created.ID, "--limit", "2000")
	if err != nil {
		t.Fatalf("ledger list --limit 2000: %v", err)
	}
	var every []ledger.Entry
	if err := json.Unmarshal([]byte(all), &every); err != nil {
		t.Fatalf("stdout is not a JSON array: %v", err)
	}
	total := len(every)
	if total <= ledger.DefaultListLimit {
		t.Fatalf("total=%d, want more than the default limit", total)
	}
	if stderr != "" {
		t.Errorf("no rows omitted, but stderr = %q", stderr)
	}

	stdout, stderr, err := runRootSplit(t, "", "ledger", "list", "--story", created.ID)
	if err != nil {
		t.Fatalf("ledger list: %v", err)
	}
	var got []ledger.Entry
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout must stay a JSON array: %v\n%s", err, stdout)
	}
	if len(got) != ledger.DefaultListLimit || got[len(got)-1].Body != "row-250" {
		t.Fatalf("got %d rows ending %q, want %d ending row-250", len(got), got[len(got)-1].Body, ledger.DefaultListLimit)
	}
	for _, want := range []string{
		fmt.Sprintf("%d older rows omitted", total-ledger.DefaultListLimit),
		fmt.Sprintf("--limit %d", total),
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %q", want, stderr)
		}
	}
	if strings.Contains(strings.TrimSpace(stderr), "\n") {
		t.Errorf("want exactly one notice line, got %q", stderr)
	}

	_, stderr, err = runRootSplit(t, "", "ledger", "list", "--story", created.ID, "--limit", fmt.Sprint(total))
	if err != nil {
		t.Fatalf("ledger list --limit %d: %v", total, err)
	}
	if stderr != "" {
		t.Errorf("a limit that fits must print no notice, got %q", stderr)
	}
}
