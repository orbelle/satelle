package cli

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

func attachments(t *testing.T, id string) []item.Attachment {
	t.Helper()
	var atts []item.Attachment
	if err := json.Unmarshal([]byte(must(t, "story", "attachments", id, "--json")), &atts); err != nil {
		t.Fatal(err)
	}
	return atts
}
