package forge

import (
	"errors"
	"strings"
	"testing"
)

const crit = "1. The node approves.\n2. go test ./... passes."

// The block round-trips: rendered into a body among other text, it is read back
// as the story's criteria with their hash, line endings rewritten or not.
func TestCriteriaBlockRoundTrips(t *testing.T) {
	block := RenderCriteria("sty_a1", "Approve PRs", crit)
	if !strings.Contains(block, "sty_a1") || !strings.Contains(block, CriteriaHash(crit)) {
		t.Fatalf("block = %q, want the story id and the criteria hash", block)
	}
	for name, body := range map[string]string{
		"lf":   "Opens the slice.\n\n" + block + "\nTrailer.",
		"crlf": strings.ReplaceAll("Opens the slice.\n\n"+block, "\n", "\r\n"),
	} {
		c, err := ParseCriteria(body, "sty_a1")
		if err != nil || c.Text != crit || c.Hash != CriteriaHash(crit) || c.StoryID != "sty_a1" {
			t.Errorf("%s: parse = %+v, %v; want the criteria back", name, c, err)
		}
	}
	if c, err := ParseCriteria(block, ""); err != nil || c.StoryID != "sty_a1" {
		t.Errorf("parse with no story = %+v, %v; want the block's own story", c, err)
	}
}

// A body with no block, a block for another story, an unclosed block and edited
// criteria are each refused with their own error.
func TestCriteriaBlockRefusals(t *testing.T) {
	block := RenderCriteria("sty_a1", "Approve PRs", crit)
	if _, err := ParseCriteria("no block here", "sty_a1"); !errors.Is(err, ErrNoCriteria) {
		t.Errorf("no block: %v, want ErrNoCriteria", err)
	}
	if _, err := ParseCriteria(strings.TrimSuffix(block, criteriaClose+"\n"), "sty_a1"); !errors.Is(err, ErrNoCriteria) {
		t.Errorf("unclosed: %v, want ErrNoCriteria", err)
	}
	var other *OtherStoryError
	if _, err := ParseCriteria(block, "sty_b2"); !errors.As(err, &other) || other.Got != "sty_a1" {
		t.Errorf("other story: %v, want an OtherStoryError naming sty_a1", err)
	}
	var mismatch *HashMismatchError
	edited := strings.Replace(block, "passes.", "passes, mostly.", 1)
	if _, err := ParseCriteria(edited, "sty_a1"); !errors.As(err, &mismatch) || mismatch.Recorded != CriteriaHash(crit) {
		t.Errorf("edited: %v, want a HashMismatchError recording the original hash", err)
	}
}

func TestSplitRef(t *testing.T) {
	if repo, n, err := SplitRef("orbelle/satelle#42"); err != nil || repo != "orbelle/satelle" || n != 42 {
		t.Errorf("SplitRef = %q %d %v", repo, n, err)
	}
	for _, bad := range []string{"orbelle/satelle", "satelle#4", "o/s#0", "o/s#x", "o/s/t#1", "o/s#04"} {
		if _, _, err := SplitRef(bad); err == nil {
			t.Errorf("SplitRef(%q) accepted", bad)
		}
	}
}
