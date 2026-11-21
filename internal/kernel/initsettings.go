package kernel

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/workflow"
)

const InitFile = "init.toml"

type InitSettings struct {
	Category string   `toml:"category"`
	Removed  []string `toml:"removed"`
}

func (s InitSettings) CategorySet() bool { return strings.TrimSpace(s.Category) != "" }

func loadInit(root string) (InitSettings, []workflow.UnknownKey, error) {
	var s InitSettings
	p := filepath.Join(root, InitFile)
	if !exists(p) {
		return s, nil, nil
	}
	un, err := workflow.Decode(p, &s)
	return s, un, err
}

func LoadInit(root string) (InitSettings, error) {
	s, _, err := loadInit(root)
	return s, err
}

func InitPolicy(root string) InitSettings {
	s, _ := LoadInit(root)
	return s
}

func (s InitSettings) problems(shipped []string) []error {
	var errs []error
	for _, rel := range s.Removed {
		switch {
		case rel == SettingsFile || rel == InitFile:
			errs = append(errs, fmt.Errorf("%s: removed names %s, which a configured repo keeps", InitFile, rel))
		case !slices.Contains(shipped, rel):
			errs = append(errs, fmt.Errorf("%s: removed %q is not a file this release ships", InitFile, rel))
		}
	}
	return errs
}
