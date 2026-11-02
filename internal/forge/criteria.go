package forge

// The criteria block is how a story's acceptance criteria reach a reviewer that
// cannot read the story: a remote node holds no story, and its egress is the repo
// and the forge's API. `satelle pr body` writes the block into the PR body and
// `satelle pr view --criteria` reads it back. This file is the one owner of its
// format.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	criteriaOpen  = "<!-- satelle:criteria "
	criteriaClose = "<!-- /satelle:criteria -->"
	hashPrefix    = "sha256:"
)

// Criteria is the block read back from a PR body.
type Criteria struct {
	StoryID string
	// Text is the acceptance criteria, as the story froze them.
	Text string
	// Hash is sha256:<hex> of Text, as the block recorded it.
	Hash string
}

// CriteriaHash is sha256:<hex> of the criteria text, normalised to LF line
// endings with the surrounding blank space trimmed, so a forge that rewrites the
// body's line endings does not break it.
func CriteriaHash(text string) string {
	sum := sha256.Sum256([]byte(normaliseCriteria(text)))
	return hashPrefix + hex.EncodeToString(sum[:])
}

func normaliseCriteria(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
}

// RenderCriteria is the marked block carrying storyID's criteria: an opening
// marker naming the story and the criteria's hash, a heading line, the criteria,
// and a closing marker.
func RenderCriteria(storyID, title, criteria string) string {
	text := normaliseCriteria(criteria)
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s %s -->\n", criteriaOpen, storyID, CriteriaHash(text))
	fmt.Fprintf(&b, "**Acceptance criteria of %s** — %s\n\n", storyID, strings.TrimSpace(title))
	b.WriteString(text)
	b.WriteString("\n" + criteriaClose + "\n")
	return b.String()
}

// ErrNoCriteria is ParseCriteria's answer for a body with no criteria block.
var ErrNoCriteria = errors.New("the PR body carries no satelle criteria block")

// OtherStoryError is ParseCriteria's answer for a block that names another story.
type OtherStoryError struct{ Want, Got string }

func (e *OtherStoryError) Error() string {
	return fmt.Sprintf("the PR body's criteria block names %s, not %s", e.Got, e.Want)
}

// HashMismatchError is ParseCriteria's answer for a block whose criteria do not
// hash to the hash it records: the criteria were edited after the block was
// rendered.
type HashMismatchError struct{ Recorded, Actual string }

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("the PR body's criteria do not match their recorded hash (recorded %s, the text hashes to %s)", e.Recorded, e.Actual)
}

var openLine = regexp.MustCompile(`^<!-- satelle:criteria (\S+) (sha256:[0-9a-f]{64}) -->$`)

// ParseCriteria reads the criteria block for storyID out of a PR body (any
// story's when storyID is empty). A body with no block is ErrNoCriteria, a block
// for another story an *OtherStoryError, and criteria that do not hash to the
// recorded hash a *HashMismatchError.
func ParseCriteria(body, storyID string) (Criteria, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	var m []string
	for i, ln := range lines {
		if m = openLine.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			start = i
			break
		}
	}
	if start < 0 {
		return Criteria{}, ErrNoCriteria
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == criteriaClose {
			end = i
			break
		}
	}
	if end < 0 {
		return Criteria{}, fmt.Errorf("%w (the block opened for %s is never closed)", ErrNoCriteria, m[1])
	}
	if storyID != "" && m[1] != storyID {
		return Criteria{}, &OtherStoryError{Want: storyID, Got: m[1]}
	}
	inner := lines[start+1 : end]
	if len(inner) > 0 && strings.HasPrefix(inner[0], "**Acceptance criteria of ") {
		inner = inner[1:] // the heading line is the block's, not the criteria's
	}
	text := normaliseCriteria(strings.Join(inner, "\n"))
	if got := CriteriaHash(text); got != m[2] {
		return Criteria{}, &HashMismatchError{Recorded: m[2], Actual: got}
	}
	return Criteria{StoryID: m[1], Text: text, Hash: m[2]}, nil
}
