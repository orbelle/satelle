package kernel

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// Two versions whose sha256 share a 7-hex prefix: the prefix is ambiguous, a
// longer one picks a version.
func TestResolveAttachmentAmbiguousPrefix(t *testing.T) {
	v1, v2 := "abcdef01"+strings.Repeat("1", 56), "abcdef02"+strings.Repeat("2", 56)
	atts := []item.Attachment{
		{Name: "twin", SHA256: v1, Size: 1, MediaType: "text/plain"},
		{Name: "twin", SHA256: v2, Size: 1, MediaType: "text/plain"},
	}
	if _, err := resolveAttachment(atts, "twin", "abcdef0"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("an ambiguous prefix: %v", err)
	}
	if a, err := resolveAttachment(atts, "twin", "abcdef02"); err != nil || a.SHA256 != v2 {
		t.Fatalf("a unique prefix: %+v %v", a, err)
	}
	if a, err := resolveAttachment(atts, "twin", ""); err != nil || a.SHA256 != v2 {
		t.Fatalf("the newest: %+v %v", a, err)
	}
}
