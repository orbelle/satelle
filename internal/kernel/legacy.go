package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpgradeCommand is the command that moves a pre-1.0 repo onto v1: it archives
// the old .satelle and writes a fresh one holding the UpgradeSkill.
const UpgradeCommand = "satelle upgrade --archive"

// UpgradeSkill is the skill the fresh .satelle carries for porting the
// archived workflows (template/skills/satelle-upgrade.md).
const UpgradeSkill = "satelle-upgrade"

// legacyFiles are files a pre-1.0 satelle always wrote into .satelle and v1
// never reads: their presence marks the old format.
var legacyFiles = []string{"deployed.version", "workflows/step.toml", "workflows/done.toml", "workflows/agents.toml"}

// LegacyMarkers lists which pre-1.0 marker files exist under root.
func LegacyMarkers(root string) []string {
	var out []string
	for _, f := range legacyFiles {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// legacyProblem names the way across for a pre-1.0 .satelle, or nil.
func legacyProblem(root string) error {
	m := LegacyMarkers(root)
	if len(m) == 0 {
		return nil
	}
	return fmt.Errorf("%s is a pre-1.0 .satelle (found: %s): run `%s` to archive it and start v1 (see `satelle help upgrade`)", root, strings.Join(m, ", "), UpgradeCommand)
}
