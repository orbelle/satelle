package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bobmcallan/satelle/internal/verb"
)

// versionLine is the single source for the build-info string printed by both
// the `version` subcommand and the root `--version` / `-v` flag, so the two
// entry points cannot drift. Sourced through verb.Dispatch — the same seam
// every other command uses — so the CLI and web report identical build info.
// The version verb needs no store, so this works before any store wiring.
func versionLine() string {
	resp, err := verb.Dispatch(context.Background(), "version", nil)
	if err != nil {
		return fmt.Sprintf("satelle (version verb error: %v)", err)
	}
	var info verb.VersionInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		return fmt.Sprintf("satelle (version decode error: %v)", err)
	}
	return fmt.Sprintf("satelle %s (commit %s, built %s)", info.Version, info.Commit, info.BuildTime)
}
