package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

// renderResponse is dispatch's one output path: plain indented JSON.
func renderResponse(cmd *cobra.Command, verbName, storyID string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	return printJSON(cmd, raw)
}
