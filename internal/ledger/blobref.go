package ledger

import (
	"errors"
	"fmt"
	"os"
)

type BlobRef struct {
	Seq       int
	Event     string
	Kind      string
	SHA256    string
	What      string
	Name      string
	MediaType string
}

const (
	GapMissing = "missing"
	GapCorrupt = "corrupt"
)

type Gap struct {
	Item string
	Ref  BlobRef
	Why  string
}

func (g Gap) String() string {
	return fmt.Sprintf("gap: %s event %d %s %s blob %s (%s): %s",
		g.Item, g.Ref.Seq, g.Ref.Kind, g.Ref.Event, g.Ref.SHA256, g.Ref.What, g.Why)
}

func ValidBlobID(sha string) bool { return shaPattern.MatchString(sha) }

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
