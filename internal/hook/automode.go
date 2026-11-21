package hook

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

var ErrAutoModeUnavailable = common.ErrAutoModeUnavailable

func AutoModeLines(guidance []byte) []string { return common.AutoModeLines(guidance) }

func autoModeAdapter(harness string) (common.HookAdapter, error) {
	a, ok := integration.Hook(harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
	}
	return a, nil
}

func AutoModeDisplay(harness string) (string, error) {
	a, err := autoModeAdapter(harness)
	if err != nil {
		return "", err
	}
	return a.AutoModeDisplay()
}

func FormatAutoMode(harness string, guidance []byte) ([]byte, error) {
	a, err := autoModeAdapter(harness)
	if err != nil {
		return nil, err
	}
	return a.FormatAutoMode(guidance)
}
