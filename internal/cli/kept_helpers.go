package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// Helpers kept from files removed in the rebuild (B1).

// stopHookTimeoutSec is the timeout, in seconds, the scaffold writes on the Stop
// hook: longer than stopGateWaitDefault, so a wait ends in an answer.
const stopHookTimeoutSec = 30 * 60

// contentMatchesSHA reports whether content's sha256 matches the server value.
// Normalises quotes and an optional sha256: prefix; unknown formats return
// false so we never skip on doubt (sty_88e83180 AC6).
func contentMatchesSHA(sha string, content []byte) bool {
	sha = strings.TrimSpace(sha)
	sha = strings.Trim(sha, `"'`)
	sha = strings.TrimPrefix(sha, "sha256:")
	sha = strings.ToLower(sha)
	if len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) == sha
}

// mergeOrder puts the local-only events in event time, each after the event it
// builds on: an event is ready once its base is placed (or is not local-only), and
// the ready event with the least time goes next, a tie to the earlier in local
// order. An event of a cycle never becomes ready and is left out.
func mergeOrder(ownOrder []ledger.Entry, own map[string]ledger.Entry) []ledger.Entry {
	pos := make(map[string]int, len(ownOrder))
	children := map[string][]ledger.Entry{}
	var ready []ledger.Entry
	for i, e := range ownOrder {
		pos[e.ID] = i
		if _, ok := own[e.BaseID]; ok {
			children[e.BaseID] = append(children[e.BaseID], e)
		} else {
			ready = append(ready, e)
		}
	}
	var merged []ledger.Entry
	for len(ready) > 0 {
		next := 0
		for i, e := range ready {
			if best := ready[next]; e.CreatedAt.Before(best.CreatedAt) || (e.CreatedAt.Equal(best.CreatedAt) && pos[e.ID] < pos[best.ID]) {
				next = i
			}
		}
		e := ready[next]
		ready = slices.Delete(ready, next, next+1)
		merged = append(merged, e)
		ready = append(ready, children[e.ID]...)
	}
	return merged
}

// matchingPrefix counts the events a local chain in chain order and a hosted chain
// share from their start: same id, same base.
func matchingPrefix(order []ledger.Entry, events []hosted.ChainEvent) int {
	n := 0
	for n < len(order) && n < len(events) && order[n].ID == events[n].ID && order[n].BaseID == events[n].BaseID {
		n++
	}
	return n
}

// resolveTeamWorkspaceName returns the team workspace name for publish (and
// similar team-bound verbs): --workspace wins, else the configured active
// workspace when it is a team, else "". Sync's rows never use this as a
// destination; only a shared stories scope's attachments do (sty_61872918).
func resolveTeamWorkspaceName(cfg config.Config, workspaceArg string) string {
	if ws := strings.TrimSpace(workspaceArg); ws != "" {
		return ws
	}
	active := cfg.ResolveActiveWorkspace()
	if active.IsPersonal() {
		return ""
	}
	return active.Name
}

// managedEditExemptEntries is the binary's deployed footprint the edit gate
// seeds as exempt: the managed .gitignore block and the harness scaffolds.
var managedEditExemptEntries = []string{".gitignore", ".claude/", ".grok/"}

// managedDraftExemptPrefixes are out-of-tree drafting locations seeded exempt.
var managedDraftExemptPrefixes = []string{"/tmp/"}

// defaultEditExemptPaths is the list init seeds: authored substrate, the managed
// footprint, and out-of-tree draft prefixes.
func defaultEditExemptPaths() []string {
	return append(append([]string{".satelle/"}, managedEditExemptEntries...), managedDraftExemptPrefixes...)
}

// defaultEditExemptTOML renders defaultEditExemptPaths as a TOML array literal.
func defaultEditExemptTOML() string {
	quoted := make([]string, 0, len(defaultEditExemptPaths()))
	for _, p := range defaultEditExemptPaths() {
		quoted = append(quoted, `"`+p+`"`)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// managedEditExemptGlobs are story-reference dump names agents write as a
// working copy of a story body/ACs.
var managedEditExemptGlobs = []string{"sty_*_body.md", "sty_*_ac.md"}

func defaultEditExemptGlobsTOML() string {
	quoted := make([]string, 0, len(managedEditExemptGlobs))
	for _, p := range managedEditExemptGlobs {
		quoted = append(quoted, `"`+p+`"`)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// harnessFootprint is every harness dir satelle deploys into.
func harnessFootprint() []string {
	return append(append([]string{}, managedEditExemptEntries...), ".pi/")
}

// gitListed reports whether a git listing command names anything for its
// pathspec. A git error counts as "names nothing".
func gitListed(worktree string, args ...string) bool {
	out, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}
