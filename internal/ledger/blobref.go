package ledger

import (
	"errors"
	"fmt"
	"os"
)

// BlobRef is a blob an event on a chain references: the event's 0-based
// position, id and kind, the blob's sha256, and what the blob is. Name and
// MediaType are an attachment's name and media type, empty for a blob that
// is not an attachment (a performer output).
type BlobRef struct {
	Seq       int
	Event     string
	Kind      string
	SHA256    string
	What      string
	Name      string
	MediaType string
}

// The reasons a referenced blob is a gap in this runtime.
const (
	GapMissing = "missing"
	GapCorrupt = "corrupt"
)

// Gap is a referenced blob that is not held intact, and why.
type Gap struct {
	Item string
	Ref  BlobRef
	Why  string
}

// String is the one line every command prints for a gap.
func (g Gap) String() string {
	return fmt.Sprintf("gap: %s event %d %s %s blob %s (%s): %s",
		g.Item, g.Ref.Seq, g.Ref.Kind, g.Ref.Event, g.Ref.SHA256, g.Ref.What, g.Why)
}

// ValidBlobID reports whether sha can name a blob: 64 lowercase hex digits.
func ValidBlobID(sha string) bool { return shaPattern.MatchString(sha) }

// BlobGaps returns the refs of item's chain whose blob this store does not
// hold intact: absent (GapMissing) or holding a body whose sha256 differs
// (GapCorrupt). Any other read error is returned.
func (s *Store) BlobGaps(item string, refs []BlobRef) ([]Gap, error) {
	var gaps []Gap
	for _, r := range refs {
		why, err := s.blobState(r.SHA256)
		if err != nil {
			return gaps, err
		}
		if why != "" {
			gaps = append(gaps, Gap{Item: item, Ref: r, Why: why})
		}
	}
	return gaps, nil
}

// blobState is "" when the blob is held intact, GapMissing or GapCorrupt.
func (s *Store) blobState(sha string) (string, error) {
	_, err := s.GetBlob(sha)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, os.ErrNotExist):
		return GapMissing, nil
	case errors.Is(err, ErrCorruptBlob):
		return GapCorrupt, nil
	}
	return "", err
}
