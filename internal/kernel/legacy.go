package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const UpgradeCommand = "satelle upgrade --archive"

var legacyFiles = []string{"deployed.version", "workflows/step.toml", "workflows/done.toml", "workflows/agents.toml"}

func LegacyMarkers(root string) []string {
	var out []string
	for _, f := range legacyFiles {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func legacyProblem(root string) error {
	m := LegacyMarkers(root)
	if len(m) == 0 {
		return nil
	}
	return fmt.Errorf("%s is a pre-1.0 .satelle (found: %s): run `%s` to archive it and start v1 (see `satelle help upgrade`)", root, strings.Join(m, ", "), UpgradeCommand)
}

func retiredRules(path string) error {
	var m map[string]any
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil
	}
	if _, ok := m["rules"]; !ok {
		return nil
	}
	return fmt.Errorf("%s: [rules] reviewer_differs_from_performer is removed, with no alias: reviewer independence is this repo's configuration. Delete the [rules] section and name the check skill satelle-judge-differs-from-performer-check (satelle init writes it to .satelle/skills) on the gates you choose; see satelle help workflows", path)
}
